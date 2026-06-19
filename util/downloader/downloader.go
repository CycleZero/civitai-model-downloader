package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	defaultConcurrency = 4
	defaultMaxRetries  = 3
	defaultHTTPTimeout = 30 * time.Second
	defaultRetryBase   = time.Second
	maxBackoff         = 30 * time.Second
)

// Config describes a downloader's runtime parameters. All fields are
// optional; missing or zero values fall back to sensible defaults in
// norm(). HTTPTimeout follows the convention: 0 means no client-level
// timeout (good for large downloads), negative means "use default".
type Config struct {
	Concurrency      int               `json:"concurrency"`
	ChunkSize        int64             `json:"chunk_size"`        // 0 = auto
	MaxRetries       int               `json:"max_retries"`
	RetryBackoffBase time.Duration     `json:"retry_backoff_base"`
	HTTPTimeout      time.Duration     `json:"http_timeout"`
	Headers          map[string]string `json:"-"`
	ProxyURL         string            `json:"proxy_url,omitempty"`
	VerifyChecksum   bool              `json:"verify_checksum"`
	ExpectedSHA256   string            `json:"expected_sha256,omitempty"`
	Resume           bool              `json:"resume"`
	Logger           *zap.Logger       `json:"-"`
	OutputPath       string            `json:"-"`
	URL              string            `json:"-"`
}

func (c *Config) norm() {
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = defaultMaxRetries
	}
	if c.RetryBackoffBase <= 0 {
		c.RetryBackoffBase = defaultRetryBase
	}
	// HTTPTimeout == 0 → no timeout (kept as-is).
	// HTTPTimeout <  0 → use default (30s).
	if c.HTTPTimeout < 0 {
		c.HTTPTimeout = defaultHTTPTimeout
	}
	if c.Headers == nil {
		c.Headers = make(map[string]string)
	}
}

// Downloader orchestrates a chunked, resumable, concurrent download
// to a single output file using direct pwrite (no merge step).
type Downloader struct {
	config Config
	url    string
	output string
	client *http.Client
	logger *zap.Logger
}

// New returns a Downloader ready to call Download. The output file is
// not created until Download is invoked.
func New(url, output string, cfg *Config) *Downloader {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.norm()
	cfg.URL = url
	cfg.OutputPath = output

	tr := buildTransport(cfg)
	client := &http.Client{Transport: tr}
	if cfg.HTTPTimeout > 0 {
		client.Timeout = cfg.HTTPTimeout
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Downloader{
		config: *cfg,
		url:    url,
		output: output,
		client: client,
		logger: logger,
	}
}

func buildTransport(cfg *Config) *http.Transport {
	tr := &http.Transport{
		MaxIdleConns:          cfg.Concurrency + 4,
		MaxIdleConnsPerHost:   cfg.Concurrency + 2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    false,
		ForceAttemptHTTP2:     true,
	}

	if cfg.ProxyURL != "" {
		if u, err := url.Parse(cfg.ProxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}

	return tr
}

// Download runs the full download pipeline:
//
//  1. probe remote size / Range support / ETag
//  2. plan chunks (dynamic, many small chunks)
//  3. load resume state (chunk-granular bitmap)
//  4. open + preallocate the output file
//  5. spawn a fixed worker pool that pulls chunks from a task queue
//  6. workers pwrite directly to the output file (no merge needed)
//  7. on completion: optional SHA256 verify, persist ETag, drop state
//
// On error or cancellation, resume state is preserved so the next
// call can skip already-finished chunks.
func (d *Downloader) Download(ctx context.Context) (err error) {
	fileSize, supportsRange, etag, err := d.probe(ctx)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}

	chunkSize := d.config.ChunkSize
	if chunkSize <= 0 {
		chunkSize = autoChunkSize(fileSize, d.config.Concurrency)
	}

	chunks := planChunks(fileSize, chunkSize)

	// Server doesn't advertise Range support: collapse to a single
	// unbounded chunk and force single-worker streaming. The worker
	// won't send a Range header (see worker.supportsRange).
	if !supportsRange {
		chunks = []Chunk{{Index: 0, Start: 0, End: -1}}
		chunkSize = 0
	}

	completed := make([]bool, len(chunks))
	var resumeBytes int64
	var resumeDoneCount int
	resumeFound := false

	if d.config.Resume && fileSize > 0 && supportsRange {
		st := loadResumable(d.url, d.output, fileSize, chunkSize, etag)
		if st != nil && len(st.Completed) == len(chunks) {
			// Verify the output file actually exists and is at least
			// fileSize bytes — if it was deleted or truncated, the
			// state's "completed" bitmap is stale and must be ignored.
			if fi, err := os.Stat(d.output); err == nil && fi.Size() >= fileSize {
				completed = st.Completed
				for i, done := range completed {
					if done {
						sz := chunks[i].Size()
						if sz > 0 {
							resumeBytes += sz
						}
						resumeDoneCount++
					}
				}
				resumeFound = true
				d.logger.Sugar().Infof("resuming: %d/%d chunks already done (%s)",
					resumeDoneCount, len(chunks), bytesHuman(float64(resumeBytes)))
			} else {
				d.logger.Sugar().Info("resume state found but output file missing/truncated — starting fresh")
			}
		}
	}

	// Open + preallocate the output file. Truncate ensures sparse
	// regions are well-defined before pwrite fills them in.
	if err := os.MkdirAll(filepath.Dir(d.output), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(d.output, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer f.Close()

	if !resumeFound {
		// Clear any stale content from a previous/aborted download.
		// For known sizes, Truncate(fileSize) both shrinks and extends.
		// For unknown sizes, Truncate(0) prevents stale tail data.
		if fileSize > 0 {
			if err := f.Truncate(fileSize); err != nil {
				return fmt.Errorf("truncate: %w", err)
			}
		} else if err := f.Truncate(0); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	} else if fileSize > 0 {
		// Resuming: ensure the file is exactly fileSize (it might be
		// larger if something appended to it, which would leave gaps
		// that pwrite won't fill).
		if err := f.Truncate(fileSize); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	}

	// Build the pending-chunk task queue.
	pending := 0
	for _, done := range completed {
		if !done {
			pending++
		}
	}
	taskCh := make(chan Chunk, pending)
	for i, ch := range chunks {
		if !completed[i] {
			taskCh <- ch
		}
	}
	close(taskCh)

	// Progress bar.
	progCh := make(chan Progress, d.config.Concurrency*2)
	pb := NewProgressBar(len(chunks), fileSize)
	if resumeBytes > 0 || resumeDoneCount > 0 {
		pb.SetCompleted(resumeBytes, resumeDoneCount)
	}

	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		for p := range progCh {
			pb.Update(p)
		}
		fmt.Fprintln(os.Stderr)
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Incremental resume-state persistence. The bitmap is updated in
	// memory on every chunk completion; state is written to disk
	// immediately so a crash or SIGKILL never loses more than the
	// chunk currently in flight.
	//
	// stMu protects completed/writtenBytes AND serializes SaveState
	// calls — without the lock, concurrent workers would race on the
	// shared ".tmp" file inside SaveState, corrupting each other's
	// writes before the atomic rename.
	var stMu sync.Mutex
	writtenBytes := resumeBytes
	onChunkDone := func(ch Chunk, bytes int64) {
		stMu.Lock()
		defer stMu.Unlock()
		completed[ch.Index] = true
		writtenBytes += bytes
		snap := State{
			Version:      stateVersion,
			URL:          d.url,
			OutputPath:   d.output,
			TotalSize:    fileSize,
			ChunkSize:    chunkSize,
			ETag:         etag,
			Completed:    append([]bool(nil), completed...),
			WrittenBytes: writtenBytes,
		}
		if err := SaveState(stateFileName(d.output), snap); err != nil {
			d.logger.Sugar().Warnf("save state: %v", err)
		}
	}

	// Spawn a fixed-size worker pool. Each worker pulls chunks from
	// taskCh until it closes, so fast workers naturally grab more
	// chunks than slow ones — no long-tail bandwidth drop-off.
	nWorkers := d.config.Concurrency
	if nWorkers > len(chunks) {
		nWorkers = len(chunks)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}

	resultCh := make(chan chunkResult, nWorkers)
	var wg sync.WaitGroup
	for i := 0; i < nWorkers; i++ {
		wg.Add(1)
		w := &worker{
			id:            i,
			url:           d.url,
			file:          f,
			client:        d.client,
			headers:       d.config.Headers,
			logger:        d.logger,
			maxRetries:    d.config.MaxRetries,
			backoff:       d.config.RetryBackoffBase,
			supportsRange: supportsRange,
		}
		go func() {
			defer wg.Done()
			w.run(ctx, taskCh, resultCh, progCh, fileSize, onChunkDone)
		}()
	}

	// Close resultCh once every worker has exited, so the collector
	// loop below can terminate deterministically regardless of how
	// many chunks were actually processed before a cancellation.
	closerDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(resultCh)
		close(closerDone)
	}()

	// Collect results. The first failure cancels the context so that
	// in-flight workers abort promptly; their remaining results (if
	// any) are still drained here so the loop ends cleanly.
	var firstErr error
	for r := range resultCh {
		if r.Err != nil && firstErr == nil {
			firstErr = r.Err
			cancel()
		}
	}

	<-closerDone
	close(progCh)
	<-progDone

	if firstErr != nil {
		// Final state snapshot is already on disk via onChunkDone;
		// just make sure the latest writtenBytes is persisted.
		stMu.Lock()
		finalSnap := State{
			Version:      stateVersion,
			URL:          d.url,
			OutputPath:   d.output,
			TotalSize:    fileSize,
			ChunkSize:    chunkSize,
			ETag:         etag,
			Completed:    append([]bool(nil), completed...),
			WrittenBytes: writtenBytes,
		}
		stMu.Unlock()
		_ = SaveState(stateFileName(d.output), finalSnap)
		return firstErr
	}

	// Optional SHA256 verification of the assembled file.
	if d.config.VerifyChecksum && d.config.ExpectedSHA256 != "" {
		got, err := sha256File(d.output)
		if err != nil {
			return fmt.Errorf("checksum: %w", err)
		}
		if !strings.EqualFold(got, d.config.ExpectedSHA256) {
			return fmt.Errorf("sha256 mismatch: expected %s, got %s", d.config.ExpectedSHA256, got)
		}
		d.logger.Sugar().Info("sha256 verified")
	}

	if etag != "" {
		saveETag(d.output, etag)
	}
	if err := DeleteState(d.output); err != nil && !os.IsNotExist(err) {
		d.logger.Sugar().Warnf("delete state: %v", err)
	}
	return nil
}

// probe issues a HEAD request to learn the file size, Range support,
// and ETag. Falls back to a ranged GET if HEAD is unhelpful.
func (d *Downloader) probe(ctx context.Context) (fileSize int64, supportsRange bool, etag string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, d.url, nil)
	if err != nil {
		return 0, false, "", err
	}
	d.applyHeaders(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, false, "", fmt.Errorf("HEAD: %w", err)
	}
	resp.Body.Close()

	supportsRange = strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes")
	etag = resp.Header.Get("ETag")
	fileSize = resp.ContentLength

	if fileSize <= 0 {
		return d.probeWithRange(ctx)
	}
	return fileSize, supportsRange, etag, nil
}

func (d *Downloader) probeWithRange(ctx context.Context) (fileSize int64, supportsRange bool, etag string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return 0, false, "", err
	}
	req.Header.Set("Range", "bytes=0-0")
	d.applyHeaders(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, false, "", err
	}
	// Drain the body so the underlying TCP connection can be returned
	// to the idle pool for reuse by subsequent chunk downloads.
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	supportsRange = resp.StatusCode == http.StatusPartialContent
	etag = resp.Header.Get("ETag")

	if parts := parseContentRange(resp.Header.Get("Content-Range")); parts != nil && parts[2] > 0 {
		return parts[2], true, etag, nil
	}
	if cl := resp.ContentLength; cl > 0 {
		return cl, supportsRange, etag, nil
	}
	return 0, supportsRange, etag, nil
}

func (d *Downloader) applyHeaders(req *http.Request) {
	for k, v := range d.config.Headers {
		req.Header.Set(k, v)
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func saveETag(outputPath, etag string) {
	etagPath := outputPath + ".etag"
	_ = os.WriteFile(etagPath, []byte(etag), 0644)
}
