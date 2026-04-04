package util

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"civitai-model-downloader/log"

	"github.com/schollz/progressbar/v3"
	"go.uber.org/zap"
)

type DownloadStatus uint8

const (
	WaitingDownload DownloadStatus = iota
	SuccessDownload                = iota
	FailedDownload
)

const (
	ChunkStatusPending   = "pending"
	ChunkStatusCompleted = "completed"
	ChunkStatusFailed    = "failed"
)

type DownloadChunkSignal struct {
	Status DownloadStatus
	Err    error
	TID    int64
	Chunk  *DownloadChunk
	// 下载的实际字节数
	BytesWritten int64
}

func NewSuccessDownloadChunkSignal(tid int64, chunk *DownloadChunk, bytesWritten int64) *DownloadChunkSignal {
	return &DownloadChunkSignal{
		Status:       SuccessDownload,
		Chunk:        chunk,
		TID:          tid,
		BytesWritten: bytesWritten,
	}
}

func NewFailedDownloadChunkSignal(tid int64, chunk *DownloadChunk, err error) *DownloadChunkSignal {
	return &DownloadChunkSignal{
		Status: FailedDownload,
		Err:    err,
		TID:    tid,
		Chunk:  chunk,
	}
}

type DownloadChunk struct {
	Url        string
	Start, End int64
	Index      int64
	FilePath   string
}

// 下载记录文件
type DownloadRecord struct {
	Url       string          `json:"url"`
	FileSize  int64           `json:"fileSize"`
	ChunkSize int64           `json:"chunkSize"`
	Chunks    []ChunkRecord   `json:"chunks"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type ChunkRecord struct {
	Index    int64  `json:"index"`
	Status   string `json:"status"`  // pending, completed, failed
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Retries  int    `json:"retries"`   // 重试次数
	FilePath string `json:"filePath"`  // 文件路径
}

// 获取记录文件路径
func getRecordFilePath(destPath string) string {
	return destPath + ".download.json"
}

// 保存下载记录
func saveDownloadRecord(record *DownloadRecord) error {
	record.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化记录失败: %w", err)
	}
	return os.WriteFile(getRecordFilePath(record.Chunks[0].FilePath), data, 0644)
}

// 加载下载记录
func loadDownloadRecord(url string, destPath string) (*DownloadRecord, error) {
	recordPath := getRecordFilePath(destPath)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return nil, err
	}
	var record DownloadRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("解析记录文件失败: %w", err)
	}
	return &record, nil
}

// 删除下载记录
func deleteDownloadRecord(destPath string) error {
	recordPath := getRecordFilePath(destPath)
	if _, err := os.Stat(recordPath); err == nil {
		return os.Remove(recordPath)
	}
	return nil
}

// 获取文件大小
func getFileSize(url string) (int64, error) {
	resp, err := GetHttpClient().c.Head(url)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("获取文件大小失败: %s", resp.Status)
	}
	defer resp.Body.Close()
	return resp.ContentLength, nil
}

// downloadChunk 下载文件块
func downloadChunk(ctx context.Context, chunk *DownloadChunk, tid int64, chunkChan chan<- *DownloadChunkSignal) {
	log.Logger().Info(fmt.Sprintf("线程 %d 开始下载块: start=%d, end=%d", chunk.Index, chunk.Start, chunk.End))

	req, err := http.NewRequestWithContext(ctx, "GET", chunk.Url, nil)
	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)
	req.Header.Set("User-Agent", "pan.baidu.com")

	resp, err := GetHttpClient().Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			log.Logger().Info(fmt.Sprintf("线程 %d 下载任务已取消", chunk.Index))
			return
		}
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}
	defer resp.Body.Close()

	// 验证 Content-Range 头
	if resp.StatusCode == http.StatusPartialContent {
		contentRange := resp.Header.Get("Content-Range")
		if contentRange != "" {
			parts := strings.Split(contentRange, " ")
			if len(parts) >= 2 {
				rangePart := parts[1]
				rangeParts := strings.Split(rangePart, "/")
				if len(rangeParts) >= 1 {
					expectedRange := fmt.Sprintf("%d-%d", chunk.Start, chunk.End)
					if !strings.HasPrefix(rangeParts[0], expectedRange) {
						err := fmt.Errorf("Content-Range 不匹配: 期望 %s, 实际 %s", expectedRange, rangeParts[0])
						chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
						return
					}
				}
			}
		}
	}

	// 打开文件并在指定位置写入数据
	file, err := os.OpenFile(chunk.FilePath, os.O_WRONLY, 0644)
	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	// 使用文件锁保护写入操作
	fileLock := sync.Mutex{}
	fileLock.Lock()

	// 设置写入位置
	_, err = file.Seek(chunk.Start, 0)
	if err != nil {
		fileLock.Unlock()
		file.Close()
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	// 使用带限流的 writer 来避免频繁的锁操作
	writer := &lockedWriter{
		file: &fileLock,
		w:    file,
	}

	written, err := io.Copy(writer, resp.Body)

	fileLock.Unlock()
	file.Close()

	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	expectedSize := chunk.End - chunk.Start + 1
	if written != expectedSize {
		err := fmt.Errorf("下载文件块长度错误: 期望 %d 字节, 实际写入 %d 字节", expectedSize, written)
		log.Logger().Error(err.Error(), zap.Int64("index", chunk.Index))
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	log.Logger().Info(fmt.Sprintf("线程 %d 下载块完成: start=%d, end=%d, bytes=%d", chunk.Index, chunk.Start, chunk.End, written))
	chunkChan <- NewSuccessDownloadChunkSignal(tid, chunk, written)
}

// lockedWriter 是一个线程安全的文件写入包装器
type lockedWriter struct {
	file *sync.Mutex
	w    *os.File
}

func (lw *lockedWriter) Write(p []byte) (n int, err error) {
	lw.file.Lock()
	defer lw.file.Unlock()
	return lw.w.Write(p)
}

// StartDownloadFile 主下载函数（支持断点续传）
func StartDownloadFile(url string, destPath string, filesize int64, numThreads int, maxChunkSize int64) error {
	return startDownloadFileWithRetry(url, destPath, filesize, numThreads, maxChunkSize, 3)
}

// StartDownloadFileWithRetry 支持重试的下载函数
func startDownloadFileWithRetry(url string, destPath string, filesize int64, numThreads int, maxChunkSize int64, maxRetries int) error {
	fileSize := filesize
	if fileSize == 0 {
		size, err := getFileSize(url)
		if err != nil {
			return fmt.Errorf("获取文件大小失败: %w", err)
		}
		fileSize = size
	}

	// 创建目标目录
	parentDir := filepath.Dir(destPath)
	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 尝试加载已有的下载记录
	record, isResume := tryLoadRecord(url, destPath, fileSize)
	if isResume {
		log.Logger().Info(fmt.Sprintf("检测到未完成的下载，将从断点恢复 (已完成 %d/%d 个分块)",
			countCompletedChunks(record.Chunks), len(record.Chunks)))
	}

	// 如果不是恢复下载，创建新记录
	if record == nil {
		record = createNewRecord(url, destPath, fileSize, numThreads, maxChunkSize)
		if err := saveDownloadRecord(record); err != nil {
			return fmt.Errorf("保存下载记录失败: %w", err)
		}
	}

	// 预先创建文件并分配空间
	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	file.Close()

	// 确保文件大小正确
	info, _ := os.Stat(destPath)
	if info == nil || info.Size() != fileSize {
		if err := os.Truncate(destPath, fileSize); err != nil {
			return fmt.Errorf("预分配文件空间失败: %w", err)
		}
	}

	// 开始下载
	return doDownloadWithRetry(record, destPath, maxRetries)
}

// 尝试加载下载记录
func tryLoadRecord(url string, destPath string, fileSize int64) (*DownloadRecord, bool) {
	record, err := loadDownloadRecord(url, destPath)
	if err != nil || record == nil {
		return nil, false
	}

	// 验证记录是否匹配
	if record.Url != url || record.FileSize != fileSize {
		return nil, false
	}

	// 检查是否所有分块都已完成
	allCompleted := true
	for i := range record.Chunks {
		if record.Chunks[i].Status != ChunkStatusCompleted {
			allCompleted = false
			break
		}
	}

	if allCompleted {
		// 下载已完成，清理记录文件
		deleteDownloadRecord(destPath)
		return nil, false
	}

	return record, true
}

// 创建新的下载记录
func createNewRecord(url string, destPath string, fileSize int64, numThreads int, maxChunkSize int64) *DownloadRecord {
	chunkSize := fileSize / int64(numThreads)
	if chunkSize > maxChunkSize || chunkSize <= 0 {
		chunkSize = maxChunkSize
	}

	numChunks := int((fileSize + chunkSize - 1) / chunkSize)
	chunks := make([]ChunkRecord, numChunks)

	for i := 0; i < numChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == numChunks-1 {
			end = fileSize - 1
		}
		chunks[i] = ChunkRecord{
			Index:    int64(i),
			Status:   ChunkStatusPending,
			Start:    start,
			End:      end,
			Retries:  0,
			FilePath: destPath,
		}
	}

	return &DownloadRecord{
		Url:       url,
		FileSize:  fileSize,
		ChunkSize: chunkSize,
		Chunks:    chunks,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// 执行下载（支持重试）
func doDownloadWithRetry(record *DownloadRecord, destPath string, maxRetries int) error {
	for {
		// 检查是否所有分块都完成
		pendingChunks := getPendingChunks(record)
		if len(pendingChunks) == 0 {
			break
		}

		// 执行一轮下载
		hasError := doDownloadRound(record, destPath)

		// 重新加载记录（可能已被其他协程更新）
		record, _ = loadDownloadRecord(record.Url, destPath)
		if record == nil {
			return fmt.Errorf("下载记录丢失")
		}

		// 检查是否有失败的块需要重试
		failedChunks := getFailedChunks(record)
		if len(failedChunks) > 0 {
			// 有失败的块，尝试重试
			canRetry := false
			for i := range record.Chunks {
				if record.Chunks[i].Status == ChunkStatusFailed {
					if record.Chunks[i].Retries < maxRetries {
						record.Chunks[i].Status = ChunkStatusPending
						record.Chunks[i].Retries++
						canRetry = true
					}
				}
			}

			if canRetry {
				if err := saveDownloadRecord(record); err != nil {
					return fmt.Errorf("保存下载记录失败: %w", err)
				}
				log.Logger().Info(fmt.Sprintf("分块失败，将进行重试"))
				continue
			} else {
				// 达到最大重试次数
				log.Logger().Error(fmt.Sprintf("部分分块下载失败，已达到最大重试次数"))
				return fmt.Errorf("下载失败: 部分分块下载失败，已达到最大重试次数")
			}
		}

		if hasError {
			// 没有失败的块但有错误，可能是所有块都完成了
			break
		}
	}

	// 验证文件完整性
	info, err := os.Stat(destPath)
	if err != nil {
		return fmt.Errorf("检查文件失败: %w", err)
	}
	if info.Size() != record.FileSize {
		return fmt.Errorf("文件大小不匹配: 期望 %d 字节, 实际 %d 字节", record.FileSize, info.Size())
	}

	// 删除记录文件
	deleteDownloadRecord(destPath)
	log.Logger().Info(fmt.Sprintf("下载完成: %s (%d 字节)", destPath, record.FileSize))

	return nil
}

// 执行一轮下载
func doDownloadRound(record *DownloadRecord, destPath string) bool {
	// 使用原子操作跟踪已下载字节数
	downloadedBytes := atomic.Int64{}
	
	// 计算已完成的数量
	completedCount := int64(countCompletedChunks(record.Chunks))
	downloadedBytes.Store(completedCount * record.ChunkSize)

	// 创建进度条
	bar := progressbar.NewOptions64(
		record.FileSize,
		progressbar.OptionSetDescription("下载进度"),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(20),
		progressbar.OptionFullWidth(),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stdout, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetRenderBlankState(true),
	)
	bar.Add64(downloadedBytes.Load())

	// 进度更新上下文
	progressCtx, stopProgress := context.WithCancel(context.Background())
	
	// 进度更新 goroutine
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer stopProgress()

		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				bar.Add64(0) // 触发进度条重绘
			}
		}
	}()

	var wg sync.WaitGroup
	sigChan := make(chan *DownloadChunkSignal, len(record.Chunks))

	// 启动下载协程（只下载 pending 状态的块）
	ctx, cancel := context.WithCancel(context.Background())

	for i := range record.Chunks {
		if record.Chunks[i].Status == ChunkStatusPending {
			wg.Add(1)
			go func(chunkRecord *ChunkRecord) {
				defer wg.Done()
				chunk := &DownloadChunk{
					Url:      record.Url,
					Start:    chunkRecord.Start,
					End:      chunkRecord.End,
					Index:    chunkRecord.Index,
					FilePath: destPath,
				}
				downloadChunk(ctx, chunk, chunkRecord.Index, sigChan)
			}(&record.Chunks[i])
		}
	}

	// 等待下载完成或出错
	go func() {
		wg.Wait()
		close(sigChan)
	}()

	var downloadErr error
	hasAnyError := false

	for signal := range sigChan {
		if signal.Status == SuccessDownload {
			// 更新记录
			for i := range record.Chunks {
				if record.Chunks[i].Index == signal.TID {
					record.Chunks[i].Status = ChunkStatusCompleted
					break
				}
			}
			downloadedBytes.Add(signal.BytesWritten)
			bar.Add64(signal.BytesWritten)

			// 定期保存记录
			if err := saveDownloadRecord(record); err != nil {
				log.Logger().Error(fmt.Sprintf("保存下载记录失败: %v", err))
			}
		} else {
			hasAnyError = true
			// 更新记录
			for i := range record.Chunks {
				if record.Chunks[i].Index == signal.TID {
					record.Chunks[i].Status = ChunkStatusFailed
					break
				}
			}
			log.Logger().Error(fmt.Sprintf("线程 %d 下载失败: %v", signal.TID, signal.Err))
			
			if downloadErr == nil {
				downloadErr = signal.Err
				cancel() // 取消其他下载
			}
		}
	}

	// 等待所有 goroutine 结束
	wg.Wait()
	stopProgress()

	// 保存最终状态
	saveDownloadRecord(record)

	return hasAnyError && downloadErr != nil
}

// 获取待下载的分块
func getPendingChunks(record *DownloadRecord) []*ChunkRecord {
	var pending []*ChunkRecord
	for i := range record.Chunks {
		if record.Chunks[i].Status == ChunkStatusPending {
			pending = append(pending, &record.Chunks[i])
		}
	}
	return pending
}

// 获取失败的分块
func getFailedChunks(record *DownloadRecord) []*ChunkRecord {
	var failed []*ChunkRecord
	for i := range record.Chunks {
		if record.Chunks[i].Status == ChunkStatusFailed {
			failed = append(failed, &record.Chunks[i])
		}
	}
	return failed
}

// 统计已完成的分块数
func countCompletedChunks(chunks []ChunkRecord) int {
	count := 0
	for i := range chunks {
		if chunks[i].Status == ChunkStatusCompleted {
			count++
		}
	}
	return count
}

// PreallocateFile 预分配文件空间
func PreallocateFile(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}

	err := file.Truncate(size)
	if err != nil {
		log.Logger().Error("Truncate预分配文件空间失败", zap.Error(err))
		// 备用方案：手动扩展文件
		_, err = file.Seek(size-1, 0)
		if err != nil {
			return err
		}
		_, err = file.Write([]byte{0})
		if err != nil {
			return err
		}
		_, err = file.Seek(0, 0)
		return err
	}
	log.Logger().Info("预分配文件空间成功")
	return nil
}

// FileExists 检查文件是否存在
func FileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

// CreateFile 创建文件及父目录
func CreateFile(path string) (*os.File, error) {
	parentDir := filepath.Dir(path)

	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("创建文件失败: %w", err)
	}

	return file, nil
}

// DownloadDirect 单线程直接下载（适用于小文件，不支持断点续传）
func DownloadDirect(url string, destPath string, filesize int64) error {
	fileSize := filesize
	if fileSize == 0 {
		size, err := getFileSize(url)
		if err != nil {
			return fmt.Errorf("获取文件大小失败: %w", err)
		}
		fileSize = size
	}

	// 创建目标目录
	parentDir := filepath.Dir(destPath)
	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 预先创建文件并分配空间
	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}

	// 预分配文件空间
	err = file.Truncate(fileSize)
	if err != nil {
		file.Close()
		return fmt.Errorf("预分配文件空间失败: %w", err)
	}
	file.Close()

	// 创建进度条
	bar := progressbar.NewOptions64(
		fileSize,
		progressbar.OptionSetDescription("下载进度"),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(20),
		progressbar.OptionFullWidth(),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stdout, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetRenderBlankState(true),
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "pan.baidu.com")

	res, err := GetHttpClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer res.Body.Close()

	// 打开文件进行写入
	file, err = os.OpenFile(destPath, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}

	_, err = io.Copy(io.MultiWriter(file, bar), res.Body)
	file.Close()

	if err != nil {
		os.Remove(destPath)
		return fmt.Errorf("下载文件失败: %w", err)
	}

	// 验证文件大小
	info, err := os.Stat(destPath)
	if err != nil {
		return fmt.Errorf("检查文件失败: %w", err)
	}
	if info.Size() != fileSize {
		os.Remove(destPath)
		return fmt.Errorf("文件大小不匹配: 期望 %d 字节, 实际 %d 字节", fileSize, info.Size())
	}

	return nil
}
