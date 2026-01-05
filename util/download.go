package util

// ... existing code ...
import (
	"civitai-model-downloader/log"
	"context"
	"fmt"
	"github.com/schollz/progressbar/v3"
	"go.uber.org/zap"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type DownloadStatus uint8

const (
	WaitingDownload DownloadStatus = iota
	SuccessDownload                = iota
	FailedDownload
)

type DownloadChunkSignal struct {
	Status DownloadStatus
	Err    error
	//goroutine 序号
	TID   int64
	Chunk *DownloadChunk
}

func NewSuccessDownloadChunkSignal(tid int64, chunk *DownloadChunk) *DownloadChunkSignal {
	return &DownloadChunkSignal{
		Status: SuccessDownload,
		Chunk:  chunk,
		TID:    tid,
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
	status     DownloadStatus
}

type ChunkData struct {
	Data  []byte
	Size  int64
	Start int64
	End   int64
	Index int64
}

// 获取文件大小
func getFileSize(url string) (int64, error) {
	resp, err := httpClient.c.Head(url)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("获取文件大小失败: %s", resp.Status)
	}
	defer resp.Body.Close()
	return resp.ContentLength, nil
}

// 下载文件块
func downloadChunk(ctx context.Context, chunk *DownloadChunk, tid int64, chunkChan chan<- *DownloadChunkSignal, bar *progressbar.ProgressBar) {
	log.Logger().Sugar().Info("线程", chunk.Index, " 下载文件块", "start: ", chunk.Start, "end: ", chunk.End)
	req, _ := http.NewRequest("GET", chunk.Url, nil)
	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)
	req.Header.Set("User-Agent", "pan.baidu.com")
	req.WithContext(ctx)
	resp, err := GetHttpClient().Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			log.Logger().Error("下载任务取消")
			return
		}
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}
	defer resp.Body.Close()

	// 打开文件并在指定位置写入数据
	file, err := os.OpenFile(chunk.FilePath, os.O_WRONLY, 0644)
	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}
	defer file.Close()

	// 设置写入位置
	_, err = file.Seek(chunk.Start, 0)
	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	// 从响应流读取并写入文件，同时更新进度条
	writen, err := io.Copy(io.MultiWriter(file, bar), resp.Body)
	if writen != chunk.End-chunk.Start+1 {
		log.Logger().Error("下载文件块长度错误", zap.Int64("index", chunk.Index), zap.Int64("writen", writen), zap.Int64("end-start+1", chunk.End-chunk.Start+1))

		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, fmt.Errorf("下载文件块长度错误: %d != %d", writen, chunk.End-chunk.Start+1))
		return
	}

	log.Logger().Sugar().Info("线程", chunk.Index, " 下载文件块完成", "开始: ", chunk.Start, " 结束: ", chunk.End)
	if err != nil {
		chunkChan <- NewFailedDownloadChunkSignal(tid, chunk, err)
		return
	}

	chunkChan <- NewSuccessDownloadChunkSignal(tid, chunk)
}

// 主下载函数
func StartDownloadFile(url string, filepath string, filesize int64, numThreads int, maxChunkSize int64) error {
	fileSize := filesize
	if fileSize == 0 {
		size, err := getFileSize(url)
		if err != nil {
			return fmt.Errorf("获取文件大小失败: %w", err)
		}
		fileSize = size
	}

	chunkSize := fileSize / int64(numThreads)
	if chunkSize > maxChunkSize || chunkSize <= 0 {
		chunkSize = maxChunkSize
	}

	numChunks := int((fileSize + chunkSize - 1) / chunkSize) // 向上取整计算块数

	//构建chunk列表
	chunkList := make([]*DownloadChunk, numChunks)
	for i := 0; i < numChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == numChunks-1 {
			end = fileSize - 1 // 最后一块下载到文件末尾
		}
		chunkList[i] = &DownloadChunk{
			Url:      url,
			Start:    start,
			End:      end,
			Index:    int64(i),
			FilePath: filepath,
		}
	}

	chunkQueue := make(chan *DownloadChunk, numChunks)
	for i := 0; i < numChunks; i++ {
		chunkQueue <- chunkList[i]
	}
	defer close(chunkQueue)

	// 预先创建文件并分配空间
	file, err := CreatFile(filepath)
	if err != nil {
		return err
	}

	// 预分配文件空间
	err = PreallocateFile(file, fileSize)
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

	semaphore := make(chan struct{}, numThreads)
	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan *DownloadChunkSignal, numChunks)
	isCompleted := false

	completedNum := atomic.Int32{}
	completedNum.Store(0)

	//for i := 0; i < numChunks; i++ {
	//	wg.Add(1)
	//	start := int64(i) * chunkSize
	//	end := start + chunkSize - 1
	//	if i == numChunks-1 {
	//		end = fileSize - 1 // 最后一块下载到文件末尾
	//	}
	//	semaphore <- struct{}{}
	//	go func(idx int64, start, end int64) {
	//		defer wg.Done()
	//		defer func() { <-semaphore }()
	//		downloadChunk(url, start, end, idx, filepath, errChan, bar)
	//	}(int64(i), start, end)
	//}

	for {
		if isCompleted {
			break
		}
		select {
		case <-ctx.Done():
			cancel()
			return fmt.Errorf("下载任务取消")

		case signal := <-sigChan:
			log.Logger().Sugar().Info("接收到线程", signal.TID, " 下载结果")
			if signal.Status == SuccessDownload {
				log.Logger().Sugar().Info("线程", signal.TID, " 下载完成")
				completedNum.Add(1)
				if completedNum.Load() == int32(numChunks) {
					log.Logger().Info("下载完成")
					isCompleted = true
					break
				}
			} else {
				log.Logger().Sugar().Error("线程", signal.TID, " 下载出错")
				cancel()
			}
		case semaphore <- struct{}{}:
			chunk := <-chunkQueue
			go func() {
				downloadChunk(ctx, chunk, chunk.Index, sigChan, bar)
				<-semaphore
			}()
		}

	}

	// 等待所有下载完成

	// 检查是否有错误
	log.Logger().Info("下载完成")
	cancel()
	return nil
}

// 预分配文件空间
func PreallocateFile(file *os.File, size int64) error {
	if size <= 0 {
		log.Logger().Error("文件大小小于等于0")
		return nil
	}

	// 尝试使用Truncate预分配空间
	err := file.Truncate(size)
	if err != nil {
		log.Logger().Error("Truncate预分配文件空间失败，正在循环填充预分配空间", zap.Error(err))
		// 如果Truncate失败，则尝试手动填充文件
		// 先回到文件开头
		_, err := file.Seek(size-1, 0)
		if err != nil {
			return err
		}

		// 写入一个字节以确保文件具有正确的大小
		_, err = file.Write([]byte{0})
		if err != nil {
			return err
		}

		// 回到文件开头
		_, err = file.Seek(0, 0)
		return err
	}
	log.Logger().Info("预分配文件空间成功")

	return nil
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return false
}

func CreatFile(path string) (*os.File, error) {
	parentDir := filepath.Dir(path)

	// 确保父目录存在，如果不存在则创建
	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	// 创建文件
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("创建文件失败: %w", err)
	}

	return file, nil
}

func DownloadDirect(url string, filepath string, filesize int64) error {
	fileSize := filesize
	if fileSize == 0 {
		size, err := getFileSize(url)
		if err != nil {
			return fmt.Errorf("获取文件大小失败: %w", err)
		}
		fileSize = size
	}
	// 预先创建文件并分配空间
	file, err := CreatFile(filepath)
	if err != nil {
		return err
	}

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
	res, err := GetHttpClient().Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer res.Body.Close()
	_, err = io.Copy(file, io.TeeReader(res.Body, bar))
	if err != nil {
		return fmt.Errorf("下载文件失败: %w", err)
	}
	return nil
}
