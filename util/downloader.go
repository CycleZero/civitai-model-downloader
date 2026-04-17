package util

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Config 下载器配置
type Config struct {
	// 并发连接数（分块数量）
	Concurrency int
	// 每块下载失败的最大重试次数
	MaxRetries int
	// 重试间隔基础时间（将使用指数退避）
	RetryBackoffBase time.Duration
	// HTTP请求超时时间
	HTTPTimeout time.Duration
	// 是否验证文件完整性（SHA256）
	VerifyChecksum bool
	// 期望的文件SHA256值
	ExpectedSHA256 string
	// 自定义HTTP Headers
	Headers map[string]string

	Logger *zap.Logger
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	return &Config{
		Concurrency:      4,
		MaxRetries:       3,
		RetryBackoffBase: 500 * time.Millisecond,
		HTTPTimeout:      30 * time.Second,
		VerifyChecksum:   false,
		ExpectedSHA256:   "",
		Headers:          make(map[string]string),
		Logger:           logger,
	}
}

// Chunk 代表一个下载分块
type Chunk struct {
	Index int   // 分块索引
	Start int64 // 起始字节（包含）
	End   int64 // 结束字节（包含）
	Done  bool  // 是否已完成
}

// Metadata 持久化的下载元数据
type Metadata struct {
	URL            string  `json:"url"`
	OutputPath     string  `json:"output_path"`
	TotalSize      int64   `json:"total_size"`
	Chunks         []Chunk `json:"chunks"`
	Concurrency    int     `json:"concurrency"`
	LastUpdated    int64   `json:"last_updated"`
	ContentType    string  `json:"content_type"`
	ETag           string  `json:"etag,omitempty"`
	ChecksumSHA256 string  `json:"checksum_sha256,omitempty"`
}

// Downloader 核心下载器
type Downloader struct {
	config   *Config
	client   *http.Client
	url      string
	output   string
	metadata *Metadata
	metaFile string
	tempFile string
	progress *progressbar.ProgressBar
	mu       sync.RWMutex

	// 原子计数器，用于线程安全的进度统计
	downloaded atomic.Int64

	// 信号通道，用于优雅中断
	ctx    context.Context
	cancel context.CancelFunc

	logger *zap.Logger
}

// NewDownloader 创建新的下载器实例
func NewDownloader(url, output string, config *Config) (*Downloader, error) {
	if config == nil {
		config = DefaultConfig()
	}

	// 创建HTTP客户端，配置连接池
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: config.Concurrency + 10,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   config.HTTPTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 确定输出文件路径和临时文件路径
	outputPath := output
	if outputPath == "" {
		outputPath = filepath.Base(url)
	}

	tempFile := outputPath + ".part"
	metaFile := outputPath + ".meta"

	return &Downloader{
		config:   config,
		client:   client,
		url:      url,
		output:   outputPath,
		tempFile: tempFile,
		metaFile: metaFile,
		ctx:      ctx,
		cancel:   cancel,
		logger:   config.Logger,
	}, nil
}

// Download 执行下载（主入口）
func (d *Downloader) Download() error {
	// 1. 获取文件信息（HEAD请求）
	if err := d.getFileInfo(); err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}

	// 2. 加载或初始化元数据（断点续传支持）
	if err := d.loadOrInitMetadata(); err != nil {
		return fmt.Errorf("初始化元数据失败: %w", err)
	}

	// 3. 验证文件是否已完整下载
	if d.isComplete() {
		if err := d.finalize(); err != nil {
			return fmt.Errorf("完成下载失败: %w", err)
		}
		d.logger.Sugar().Info("\n✅ 文件已完整下载！")
		return nil
	}

	// 4. 创建/打开临时文件
	file, err := d.openTempFile()
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}
	defer file.Close()

	// 5. 初始化进度条
	if err := d.initProgressBar(); err != nil {
		return fmt.Errorf("初始化进度条失败: %w", err)
	}

	// 6. 启动信号监听（优雅中断）
	go d.handleSignals()

	// 7. 执行并发下载
	if err := d.downloadChunks(file); err != nil {
		// 保存当前进度
		d.saveMetadata()
		return fmt.Errorf("下载失败: %w", err)
	}

	// 8. 下载完成，最终化处理
	return d.finalize()
}

// getFileInfo 通过HEAD请求获取文件信息，并验证Range支持
func (d *Downloader) getFileInfo() error {
	req, err := http.NewRequestWithContext(d.ctx, http.MethodHead, d.url, nil)
	if err != nil {
		return err
	}

	// 添加自定义Headers
	for k, v := range d.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HEAD请求失败: %s", resp.Status)
	}

	// 检查是否支持Range请求
	acceptRanges := resp.Header.Get("Accept-Ranges")
	if acceptRanges != "bytes" {
		d.logger.Sugar().Infof("⚠️  警告: 服务器不支持Range请求，将降级为单线程下载\n")
		d.config.Concurrency = 1
	}

	// 获取文件总大小
	totalSize := resp.ContentLength
	if totalSize <= 0 {
		return fmt.Errorf("无法获取文件大小")
	}

	// 存储文件信息
	if d.metadata == nil {
		d.metadata = &Metadata{
			URL:         d.url,
			OutputPath:  d.output,
			TotalSize:   totalSize,
			Concurrency: d.config.Concurrency,
			Chunks:      make([]Chunk, 0),
		}
	} else {
		d.metadata.TotalSize = totalSize
	}

	d.metadata.ContentType = resp.Header.Get("Content-Type")
	d.metadata.ETag = resp.Header.Get("ETag")
	d.metadata.LastUpdated = time.Now().Unix()

	return nil
}

// loadOrInitMetadata 加载已有的元数据文件，或初始化新的
func (d *Downloader) loadOrInitMetadata() error {
	// 尝试加载已有的元数据
	data, err := os.ReadFile(d.metaFile)
	if err == nil {
		var meta Metadata
		if err := json.Unmarshal(data, &meta); err == nil {
			// 验证URL是否匹配，以及文件大小是否变化
			if meta.URL == d.url && meta.TotalSize == d.metadata.TotalSize {
				d.metadata = &meta
				d.logger.Sugar().Infof("📋 加载断点续传状态: 已完成 %d/%d 块\n",
					d.completedChunksCount(), len(meta.Chunks))
				return nil
			} else {
				d.logger.Sugar().Infof("⚠️  元数据不匹配，将重新下载\n")
				os.Remove(d.metaFile)
			}
		}
	}

	// 初始化新的分块
	chunkSize := d.metadata.TotalSize / int64(d.config.Concurrency)
	if chunkSize < 1024*1024 { // 如果单块小于1MB，减少并发数
		d.config.Concurrency = int(d.metadata.TotalSize / (1024 * 1024))
		if d.config.Concurrency < 1 {
			d.config.Concurrency = 1
		}
		chunkSize = d.metadata.TotalSize / int64(d.config.Concurrency)
	}

	chunks := make([]Chunk, 0, d.config.Concurrency)
	for i := 0; i < d.config.Concurrency; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == d.config.Concurrency-1 {
			end = d.metadata.TotalSize - 1
		}
		chunks = append(chunks, Chunk{
			Index: i,
			Start: start,
			End:   end,
			Done:  false,
		})
	}

	d.metadata.Chunks = chunks
	d.metadata.Concurrency = d.config.Concurrency
	d.metadata.LastUpdated = time.Now().Unix()

	return d.saveMetadata()
}

// openTempFile 打开临时文件（支持断点续传的Seek写入）
func (d *Downloader) openTempFile() (*os.File, error) {
	// 重要: 必须使用 O_WRONLY|O_CREATE，不能使用 O_APPEND
	// 因为断点续传需要Seek到指定偏移量写入，O_APPEND会强制写入末尾[reference:5]
	file, err := os.OpenFile(d.tempFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// 确保文件大小至少为TotalSize（预分配空间，减少碎片）
	if err := file.Truncate(d.metadata.TotalSize); err != nil {
		file.Close()
		return nil, err
	}

	return file, nil
}

// downloadChunks 并发下载所有分块
func (d *Downloader) downloadChunks(file *os.File) error {
	// 使用errgroup进行错误传播和上下文取消
	g, ctx := errgroup.WithContext(d.ctx)

	// 控制并发度的信号量
	semaphore := make(chan struct{}, d.config.Concurrency)

	// 启动下载进度更新协程
	stopProgress := make(chan struct{})
	go d.updateProgressLoop(stopProgress)

	// 遍历所有未完成的分块
	for i := range d.metadata.Chunks {
		chunk := &d.metadata.Chunks[i]
		if chunk.Done {
			// 已完成的块，累加进度
			d.downloaded.Add(chunk.End - chunk.Start + 1)
			continue
		}

		g.Go(func() error {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return ctx.Err()
			}

			// 带重试的下载单个分块
			return d.downloadChunkWithRetry(ctx, file, chunk)
		})
	}

	// 等待所有分块完成
	err := g.Wait()
	close(stopProgress)

	if err != nil {
		return err
	}

	// 验证所有分块是否都已完成
	if d.completedChunksCount() != len(d.metadata.Chunks) {
		return fmt.Errorf("部分分块下载失败")
	}

	return nil
}

// downloadChunkWithRetry 带指数退避重试的单个分块下载
func (d *Downloader) downloadChunkWithRetry(ctx context.Context, file *os.File, chunk *Chunk) error {
	var lastErr error

	for attempt := 0; attempt <= d.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避 + jitter 防止惊群效应[reference:6]
			backoff := d.config.RetryBackoffBase * time.Duration(1<<uint(attempt-1))
			jitter := time.Duration(0.7+float64(attempt)*0.1) * time.Millisecond
			backoff += jitter
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}

			d.logger.Sugar().Infof("\n🔄 重试分块 %d (第%d次)\n", chunk.Index, attempt)
		}

		if err := d.downloadChunk(ctx, file, chunk); err != nil {
			lastErr = err
			continue
		}

		// 下载成功，标记完成并保存元数据
		chunk.Done = true
		if err := d.saveMetadata(); err != nil {
			return err
		}

		return nil
	}

	return fmt.Errorf("分块 %d 重试%d次后仍然失败: %w", chunk.Index, d.config.MaxRetries, lastErr)
}

// downloadChunk 下载单个分块
func (d *Downloader) downloadChunk(ctx context.Context, file *os.File, chunk *Chunk) error {
	// 构建Range请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return err
	}

	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)

	// 添加自定义Headers
	for k, v := range d.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 验证响应状态码
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Range请求失败: %s", resp.Status)
	}

	// 验证Content-Range头
	contentRange := resp.Header.Get("Content-Range")
	if contentRange == "" && resp.StatusCode == http.StatusPartialContent {
		return fmt.Errorf("缺少Content-Range头")
	}

	// 定位到文件偏移量并写入数据
	if _, err := file.Seek(chunk.Start, io.SeekStart); err != nil {
		return fmt.Errorf("Seek失败: %w", err)
	}

	// 使用LimitReader限制读取的字节数，防止服务端返回过多数据[reference:7]
	expectedSize := chunk.End - chunk.Start + 1
	limitedReader := io.LimitReader(resp.Body, expectedSize)

	written, err := io.Copy(file, limitedReader)
	if err != nil {
		return fmt.Errorf("写入失败: %w", err)
	}

	if written != expectedSize {
		return fmt.Errorf("写入字节数不匹配: 期望%d, 实际%d", expectedSize, written)
	}

	// 强制刷盘，确保数据持久化（关键：防止断电数据丢失）[reference:8]
	if err := file.Sync(); err != nil {
		return fmt.Errorf("Sync失败: %w", err)
	}

	// 原子更新已下载字节数
	d.downloaded.Add(written)

	return nil
}

// initProgressBar 初始化进度条
func (d *Downloader) initProgressBar() error {
	desc := fmt.Sprintf("📥 下载中 %s", filepath.Base(d.output))

	d.progress = progressbar.NewOptions64(
		d.metadata.TotalSize,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(65*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stdout, "\n")
		}),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerHead:    "█",
			SaucerPadding: "░",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	// 设置初始进度
	d.progress.Set64(d.downloaded.Load())
	return nil
}

// updateProgressLoop 定期更新进度条
func (d *Downloader) updateProgressLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if d.progress != nil {
				d.progress.Set64(d.downloaded.Load())
			}
		}
	}
}

// handleSignals 处理系统信号，实现优雅中断[reference:9]
func (d *Downloader) handleSignals() {
	sigChan := make(chan os.Signal, 1)
	// 监听中断信号
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
		d.logger.Sugar().Info("\n\n⚠️  收到中断信号，正在保存进度...")
		d.saveMetadata()
		d.logger.Sugar().Infof("✅ 进度已保存，下次运行将继续下载\n")
		d.cancel()
		os.Exit(0)
	case <-d.ctx.Done():
		return
	}
}

// saveMetadata 原子保存元数据到文件
func (d *Downloader) saveMetadata() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.metadata.LastUpdated = time.Now().Unix()

	// 先写入临时文件，再重命名，实现原子操作[reference:10]
	tempMeta := d.metaFile + ".tmp"
	data, err := json.MarshalIndent(d.metadata, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tempMeta, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempMeta, d.metaFile)
}

// completedChunksCount 统计已完成的分块数量
func (d *Downloader) completedChunksCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	count := 0
	for _, chunk := range d.metadata.Chunks {
		if chunk.Done {
			count++
		}
	}
	return count
}

// isComplete 检查是否所有分块都已完成
func (d *Downloader) isComplete() bool {
	return d.completedChunksCount() == len(d.metadata.Chunks)
}

// finalize 完成下载的最终处理
func (d *Downloader) finalize() error {
	// 1. 关闭进度条
	if d.progress != nil {
		d.progress.Finish()
	}

	// 2. 验证文件完整性
	if d.config.VerifyChecksum && d.config.ExpectedSHA256 != "" {
		if err := d.verifyChecksum(); err != nil {
			return fmt.Errorf("SHA256校验失败: %w", err)
		}
		d.logger.Sugar().Info("✅ SHA256校验通过")
	}

	// 3. 原子重命名临时文件为最终文件[reference:11]
	if err := os.Rename(d.tempFile, d.output); err != nil {
		return err
	}

	// 4. 删除元数据文件
	os.Remove(d.metaFile)

	return nil
}

// verifyChecksum 验证文件的SHA256哈希值
func (d *Downloader) verifyChecksum() error {
	file, err := os.Open(d.tempFile)
	if err != nil {
		return err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}

	actualSHA256 := hex.EncodeToString(hash.Sum(nil))
	if actualSHA256 != d.config.ExpectedSHA256 {
		return fmt.Errorf("SHA256不匹配: 期望=%s, 实际=%s",
			d.config.ExpectedSHA256, actualSHA256)
	}

	d.metadata.ChecksumSHA256 = actualSHA256
	return nil
}

// GetProgress 获取当前下载进度（供外部监控使用）
func (d *Downloader) GetProgress() (downloaded int64, total int64, percentage float64) {
	downloaded = d.downloaded.Load()
	total = d.metadata.TotalSize
	if total > 0 {
		percentage = float64(downloaded) / float64(total) * 100
	}
	return
}
