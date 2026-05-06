package downloader

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

type Config struct {
	Concurrency      int               `json:"concurrency"`
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
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = defaultHTTPTimeout
	}
	if c.Headers == nil {
		c.Headers = make(map[string]string)
	}
}

type Downloader struct {
	config Config
	url    string
	output string
	client *http.Client
	logger *zap.Logger
}

func New(url, output string, cfg *Config) *Downloader {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.norm()
	cfg.URL = url
	cfg.OutputPath = output

	tr := buildTransport(cfg)
	client := &http.Client{
		Transport: tr,
		Timeout:   cfg.HTTPTimeout,
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
		MaxIdleConns:        cfg.Concurrency + 4,
		MaxIdleConnsPerHost: cfg.Concurrency + 2,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  false,
		ForceAttemptHTTP2:   true,
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

func (d *Downloader) Download(ctx context.Context) (err error) {
	fileSize, supportsRange, etag, err := d.probe(ctx)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}

	segs, err := d.segments(fileSize, supportsRange)
	if err != nil {
		return fmt.Errorf("segments: %w", err)
	}

	if d.config.Resume && fileSize > 0 {
		resumeState := findResumableState(d.url, d.output)
		if resumeState != nil && resumeState.TotalSize == fileSize && len(resumeState.Segments) == len(segs) {
			d.applyResume(segs, resumeState)
		}
	}

	if err := os.MkdirAll(filepath.Dir(d.output), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	progCh := make(chan Progress, len(segs)*2)
	pb := NewProgressBar(len(segs), fileSize)

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

	type result struct {
		idx int
		err error
	}
	resultCh := make(chan result, len(segs))

	for i := range segs {
		if segs[i].TempPath == "" {
			segs[i].TempPath = tempPath(d.url, d.output, i)
		}
		go func(seg *Segment, idx int) {
			resultCh <- result{idx, d.downloadSegment(ctx, seg, fileSize, progCh)}
		}(&segs[i], i)
	}

	results := make([]result, len(segs))
	done := 0
	for done < len(segs) {
		select {
		case r := <-resultCh:
			results[r.idx] = r
			if r.err != nil {
				cancel()
			}
			done++
		case <-ctx.Done():
			d.saveState(segs, fileSize)
			close(progCh)
			<-progDone
			return ctx.Err()
		}
	}

	close(progCh)
	<-progDone

	for i, r := range results {
		if r.err != nil {
			d.saveState(segs, fileSize)
			return fmt.Errorf("segment %d: %w", i, r.err)
		}
	}

	if err := d.merge(segs); err != nil {
		return fmt.Errorf("merge: %w", err)
	}

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

	d.cleanup(segs)
	return nil
}

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
	defer resp.Body.Close()

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

func (d *Downloader) segments(fileSize int64, supportsRange bool) ([]Segment, error) {
	n := d.config.Concurrency

	if fileSize <= 0 || !supportsRange {
		return []Segment{{
			Index: 0,
			Start: 0,
			End:   fileSize - 1,
		}}, nil
	}

	if n > int(fileSize) {
		n = int(fileSize)
	}
	if n < 1 {
		n = 1
	}

	segs := make([]Segment, n)
	chunk := fileSize / int64(n)

	var start int64
	for i := 0; i < n; i++ {
		end := start + chunk - 1
		if i == n-1 {
			end = fileSize - 1
		}
		segs[i] = Segment{Index: i, Start: start, End: end}
		start = end + 1
	}
	return segs, nil
}

func (d *Downloader) applyResume(segs []Segment, state *State) {
	for i := range segs {
		if i >= len(state.Segments) {
			continue
		}
		rs := state.Segments[i]
		segs[i].TempPath = rs.TempPath
		segs[i].DownloadedBytes = rs.DownloadedBytes
	}
}

func (d *Downloader) downloadSegment(ctx context.Context, seg *Segment, totalSize int64, progCh chan<- Progress) error {
	attempt := 0
	var lastErr error

	for attempt <= d.config.MaxRetries {
		if attempt > 0 {
			backoff := d.config.RetryBackoffBase * time.Duration(1<<(attempt-1))
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		err := seg.Download(ctx, d.url, d.client, d.config.Headers, progCh, totalSize)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		lastErr = err
		attempt++
		continue
	}

	if lastErr != nil {
		return fmt.Errorf("segment %d: retries exhausted: %w", seg.Index, lastErr)
	}
	return nil
}

func (d *Downloader) merge(segs []Segment) error {
	out, err := os.Create(d.output)
	if err != nil {
		return err
	}
	defer out.Close()

	var totalWritten int64
	for _, seg := range segs {
		f, err := os.Open(seg.TempPath)
		if err != nil {
			return fmt.Errorf("segment %d: %w", seg.Index, err)
		}
		n, err := io.Copy(out, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("segment %d: %w", seg.Index, err)
		}
		totalWritten += n
	}

	expSize := int64(0)
	for _, seg := range segs {
		expSize += seg.SegmentSize()
	}
	if expSize > 0 && totalWritten != expSize {
		return fmt.Errorf("merged size %d, expected %d", totalWritten, expSize)
	}

	return nil
}

func (d *Downloader) cleanup(segs []Segment) {
	for _, seg := range segs {
		if seg.TempPath != "" {
			os.Remove(seg.TempPath)
		}
	}
	DeleteState(d.output)
}

func (d *Downloader) saveState(segs []Segment, totalSize int64) {
	segStates := make([]SegmentState, len(segs))
	for i, seg := range segs {
		segStates[i] = SegmentState{
			Index:           seg.Index,
			Start:           seg.Start,
			End:             seg.End,
			TempPath:        seg.TempPath,
			DownloadedBytes: seg.DownloadedBytes,
		}
	}
	state := State{
		URL:        d.url,
		OutputPath: d.output,
		TotalSize:  totalSize,
		Segments:   segStates,
	}
	if err := SaveState(stateFileName(d.output), state); err != nil {
		fmt.Fprintf(os.Stderr, "save state: %v\n", err)
	}
}

func (d *Downloader) applyHeaders(req *http.Request) {
	for k, v := range d.config.Headers {
		req.Header.Set(k, v)
	}
}

func tempPath(url, output string, idx int) string {
	return filepath.Join(filepath.Dir(output), fmt.Sprintf(".%s.part%d", filepath.Base(output), idx))
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
	os.WriteFile(etagPath, []byte(etag), 0644)
}

var _ = tls.VersionTLS13
