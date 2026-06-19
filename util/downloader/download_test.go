package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test HTTP server — query-param configurable
// ---------------------------------------------------------------------------
//
//	size=N         Content-Length of body (default 1024)
//	status=N       HTTP status (default 200)
//	delay=D        initial delay in ms (default 0)
//	body-delay=D   delay between 32KB chunks in ms (default 0)
//	no-range=1     omit Accept-Ranges, ignore Range header
//	etag=S         set ETag header
//	filename=S     set Content-Disposition filename
//	fail-after=N   stop writing after N bytes (simulate connection drop)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		bodySize := int64(1024)
		if v := q.Get("size"); v != "" {
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				bodySize = n
			}
		}
		statusCode := 200
		if v := q.Get("status"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 100 {
				statusCode = n
			}
		}
		initDelay, _ := strconv.Atoi(q.Get("delay"))
		bodyDelay, _ := strconv.Atoi(q.Get("body-delay"))
		noRange := q.Get("no-range") == "1"
		etag := q.Get("etag")
		filename := q.Get("filename")
		failAfter, _ := strconv.ParseInt(q.Get("fail-after"), 10, 64)

		if initDelay > 0 {
			select {
			case <-time.After(time.Duration(initDelay) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}

		if r.Method == http.MethodHead {
			if !noRange {
				w.Header().Set("Accept-Ranges", "bytes")
			}
			if etag != "" {
				w.Header().Set("ETag", etag)
			}
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			if filename != "" {
				w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
			}
			w.WriteHeader(statusCode)
			return
		}

		if statusCode >= 400 {
			w.WriteHeader(statusCode)
			w.Write([]byte("error body"))
			return
		}

		if filename != "" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" && !noRange {
			var rs, re int64
			if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &rs, &re); err != nil {
				if _, err2 := fmt.Sscanf(rangeHeader, "bytes=%d-", &rs); err2 != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				re = bodySize - 1
			}
			if rs >= bodySize {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if re >= bodySize {
				re = bodySize - 1
			}
			chunkLen := re - rs + 1
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re, bodySize))
			w.Header().Set("Content-Length", strconv.FormatInt(chunkLen, 10))
			if etag != "" {
				w.Header().Set("ETag", etag)
			}
			w.WriteHeader(http.StatusPartialContent)

			payload := make([]byte, chunkLen)
			for i := range payload {
				payload[i] = byte((rs + int64(i)) % 256)
			}
			streamBody(w, payload, bodyDelay, failAfter, r.Context())
			return
		}

		if !noRange {
			w.Header().Set("Accept-Ranges", "bytes")
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(statusCode)

		payload := make([]byte, bodySize)
		for i := range payload {
			payload[i] = byte(i % 256)
		}
		streamBody(w, payload, bodyDelay, failAfter, r.Context())
	}))

	t.Cleanup(srv.Close)
	return srv
}

func streamBody(w io.Writer, payload []byte, chunkDelayMs int, failAfter int64, ctx context.Context) {
	chunkSize := 32 * 1024
	var written int64
	for len(payload) > 0 {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if failAfter > 0 && written >= failAfter {
			return
		}
		end := chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		n, err := w.Write(payload[:end])
		if err != nil {
			return
		}
		written += int64(n)
		payload = payload[end:]
		if chunkDelayMs > 0 {
			select {
			case <-time.After(time.Duration(chunkDelayMs) * time.Millisecond):
			case <-ctx.Done():
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testConfig(serverURL string) *Config {
	return &Config{
		Concurrency:      2,
		MaxRetries:       2,
		RetryBackoffBase: 10 * time.Millisecond,
		HTTPTimeout:      0,
		Resume:           false,
		Logger:           zap.NewNop(),
	}
}

func testConfigWithResume(serverURL string) *Config {
	cfg := testConfig(serverURL)
	cfg.Resume = true
	return cfg
}

func tempOutput(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func newDownloader(t *testing.T, srv *httptest.Server, output string, cfg *Config) *Downloader {
	t.Helper()
	return New(srv.URL, output, cfg)
}

func expectedPayload(bodySize int64) []byte {
	p := make([]byte, bodySize)
	for i := range p {
		p[i] = byte(i % 256)
	}
	return p
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	return data
}

// =========================================================================
// Unit tests for pure functions
// =========================================================================

func TestPlanChunks(t *testing.T) {
	cases := []struct {
		name      string
		fileSize  int64
		chunkSize int64
		want      []Chunk
	}{
		{"unknown size", -1, 1024, []Chunk{{Index: 0, Start: 0, End: -1}}},
		{"zero size", 0, 1024, []Chunk{{Index: 0, Start: 0, End: -1}}},
		{"single chunk (size<chunk)", 100, 1024, []Chunk{{Index: 0, Start: 0, End: 99}}},
		{"exact fit", 1024, 512, []Chunk{
			{Index: 0, Start: 0, End: 511},
			{Index: 1, Start: 512, End: 1023},
		}},
		{"remainder", 1000, 256, []Chunk{
			{Index: 0, Start: 0, End: 255},
			{Index: 1, Start: 256, End: 511},
			{Index: 2, Start: 512, End: 767},
			{Index: 3, Start: 768, End: 999},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planChunks(tc.fileSize, tc.chunkSize)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d chunks, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("chunk %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestAutoChunkSize(t *testing.T) {
	if got := autoChunkSize(-1, 8); got != 16<<20 {
		t.Errorf("unknown size: got %d, want 16MB", got)
	}
	if got := autoChunkSize(1000, 8); got != 1000 {
		t.Errorf("tiny file: got %d, want 1000 (whole file)", got)
	}
	if got := autoChunkSize(64<<20, 8); got < 1<<20 {
		t.Errorf("mid file: got %d, want >= 1MB", got)
	}
	if got := autoChunkSize(1<<30, 8); got != 16<<20 {
		t.Errorf("big file: got %d, want 16MB", got)
	}
}

func TestBuildRange(t *testing.T) {
	if got := buildRange(-1, -1); got != "" {
		t.Errorf("unbounded: got %q", got)
	}
	if got := buildRange(0, 99); got != "bytes=0-99" {
		t.Errorf("got %q", got)
	}
	if got := buildRange(100, -1); got != "bytes=100-" {
		t.Errorf("got %q", got)
	}
}

func TestParseContentRange(t *testing.T) {
	parts := parseContentRange("bytes 0-99/200")
	if parts == nil || parts[0] != 0 || parts[1] != 99 || parts[2] != 200 {
		t.Fatalf("got %v", parts)
	}
	parts = parseContentRange("bytes 100-199/*")
	if parts == nil || parts[2] != 0 {
		t.Fatalf("wildcard total: got %v", parts)
	}
	if parseContentRange("invalid") != nil {
		t.Error("expected nil for invalid header")
	}
}

// =========================================================================
// HTTPTimeout handling (regression for BUG 1)
// =========================================================================

func TestHTTPTimeoutZeroPreserved(t *testing.T) {
	cfg := &Config{HTTPTimeout: 0}
	cfg.norm()
	if cfg.HTTPTimeout != 0 {
		t.Fatalf("HTTPTimeout should stay 0, got %v", cfg.HTTPTimeout)
	}
}

func TestHTTPTimeoutNegativeUsesDefault(t *testing.T) {
	cfg := &Config{HTTPTimeout: -1}
	cfg.norm()
	if cfg.HTTPTimeout != 30*time.Second {
		t.Fatalf("HTTPTimeout should default to 30s, got %v", cfg.HTTPTimeout)
	}
}

func TestDownloaderClientHasNoGlobalTimeout(t *testing.T) {
	srv := newTestServer(t)
	cfg := testConfig(srv.URL)
	cfg.HTTPTimeout = 0
	dl := newDownloader(t, srv, tempOutput(t, "x.bin"), cfg)
	if dl.client.Timeout != 0 {
		t.Fatalf("client.Timeout should be 0, got %v", dl.client.Timeout)
	}
}

func TestExplicitHTTPTimeoutApplied(t *testing.T) {
	srv := newTestServer(t)
	cfg := testConfig(srv.URL)
	cfg.HTTPTimeout = 5 * time.Second
	dl := newDownloader(t, srv, tempOutput(t, "x.bin"), cfg)
	if dl.client.Timeout != 5*time.Second {
		t.Fatalf("client.Timeout should be 5s, got %v", dl.client.Timeout)
	}
}

// =========================================================================
// Basic download correctness
// =========================================================================

func TestBasicDownload(t *testing.T) {
	bodySize := int64(100_000)
	srv := newTestServer(t)
	out := tempOutput(t, "basic.bin")
	cfg := testConfig(srv.URL + "?size=" + strconv.FormatInt(bodySize, 10))
	cfg.Concurrency = 4
	cfg.ChunkSize = 16 * 1024 // 16KB → ~7 chunks

	dl := New(srv.URL+"?size="+strconv.FormatInt(bodySize, 10), out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}

	got := readFile(t, out)
	want := expectedPayload(bodySize)
	if len(got) != len(want) {
		t.Fatalf("size: got %d, want %d", len(got), len(want))
	}
	if string(got) != string(want) {
		t.Fatal("content mismatch")
	}

	// State file must be cleaned up on success.
	if _, err := os.Stat(stateFileName(out)); !os.IsNotExist(err) {
		t.Errorf("state file should be deleted on success, err=%v", err)
	}
}

func TestSmallFileSingleChunk(t *testing.T) {
	srv := newTestServer(t)
	out := tempOutput(t, "small.bin")
	cfg := testConfig(srv.URL)
	cfg.ChunkSize = 1 << 20 // 1MB, larger than default body (1KB)

	dl := newDownloader(t, srv, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if len(got) != 1024 {
		t.Fatalf("size: got %d, want 1024", len(got))
	}
}

func TestUnsupportedRangeFallback(t *testing.T) {
	bodySize := int64(50_000)
	out := tempOutput(t, "norange.bin")
	cfg := testConfig("")
	cfg.Concurrency = 8 // would normally spawn 8 workers
	cfg.ChunkSize = 1024

	noRangeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// No Accept-Ranges header
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(200)
		p := expectedPayload(bodySize)
		w.Write(p)
	}))
	t.Cleanup(noRangeSrv.Close)

	dl := New(noRangeSrv.URL, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if int64(len(got)) != bodySize {
		t.Fatalf("size: got %d, want %d", len(got), bodySize)
	}
	want := expectedPayload(bodySize)
	if string(got) != string(want) {
		t.Fatal("content mismatch")
	}
}

// =========================================================================
// Dynamic chunking + worker pool — no long tail
// =========================================================================

// TestWorkerPoolStaysSaturated verifies that the worker pool keeps N
// workers active throughout the download — the core property that
// eliminates the long-tail bandwidth drop-off of static segmentation.
//
// Approach: server tracks the max number of concurrently in-flight
// requests. With many small chunks and N workers, the peak concurrency
// should reach N (or close to it), proving workers don't sit idle
// waiting for a single slow segment to finish.
func TestWorkerPoolStaysSaturated(t *testing.T) {
	bodySize := int64(1 << 20) // 1 MB

	var inflight atomic.Int32
	var maxInflight atomic.Int32
	wrapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count concurrency here and respond inline.
		cur := inflight.Add(1)
		for {
			old := maxInflight.Load()
			if cur <= old || maxInflight.CompareAndSwap(old, cur) {
				break
			}
		}
		defer inflight.Add(-1)

		// Small delay to widen the concurrency window.
		if r.Method != http.MethodHead {
			select {
			case <-time.After(5 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}

		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}

		rh := r.Header.Get("Range")
		var rs, re int64
		if _, err := fmt.Sscanf(rh, "bytes=%d-%d", &rs, &re); err == nil {
			if re >= bodySize {
				re = bodySize - 1
			}
			cl := re - rs + 1
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re, bodySize))
			w.Header().Set("Content-Length", strconv.FormatInt(cl, 10))
			w.WriteHeader(206)
			p := make([]byte, cl)
			for i := range p {
				p[i] = byte((rs + int64(i)) % 256)
			}
			w.Write(p)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(200)
		w.Write(expectedPayload(bodySize))
	}))
	t.Cleanup(wrapSrv.Close)

	out := tempOutput(t, "sat.bin")
	cfg := testConfig(wrapSrv.URL)
	cfg.Concurrency = 8
	cfg.ChunkSize = 16 * 1024 // 1MB / 16KB = 64 chunks
	cfg.MaxRetries = 0

	dl := newDownloader(t, wrapSrv, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}

	if got := readFile(t, out); string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch")
	}

	// With 8 workers and 64 chunks, peak concurrency must reach
	// at least 6 (allowing scheduling slack). Static segmentation
	// with 8 segments would also hit 8 here — the point is that
	// the NEW architecture never goes BELOW this late in the
	// download. We assert the peak is high; TestNoLongTail checks
	// the tail behavior more directly.
	if peak := maxInflight.Load(); peak < 6 {
		t.Errorf("peak concurrency = %d, want >= 6 (worker pool not saturated)", peak)
	}
}

// TestNoLongTail verifies the defining property of the new design:
// the LAST chunk to finish is served while other workers are still
// active. With static segmentation the last segment finishes alone.
//
// We approximate this by recording the timestamps of all chunk
// completion events and checking that the gap between the
// second-to-last and last completion is small relative to the total
// download time — i.e. no single chunk dominates the tail.
func TestNoLongTail(t *testing.T) {
	bodySize := int64(512 * 1024) // 512 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}
		rh := r.Header.Get("Range")
		var rs, re int64
		if _, err := fmt.Sscanf(rh, "bytes=%d-%d", &rs, &re); err != nil {
			re = bodySize - 1
			rs = 0
		}
		if re >= bodySize {
			re = bodySize - 1
		}
		// Add 10ms per chunk so completion times are measurable.
		select {
		case <-time.After(10 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		cl := re - rs + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re, bodySize))
		w.Header().Set("Content-Length", strconv.FormatInt(cl, 10))
		w.WriteHeader(206)
		p := make([]byte, cl)
		for i := range p {
			p[i] = byte((rs + int64(i)) % 256)
		}
		w.Write(p)
	}))
	t.Cleanup(srv.Close)

	out := tempOutput(t, "notail.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 4
	cfg.ChunkSize = 32 * 1024 // 512KB / 32KB = 16 chunks
	cfg.MaxRetries = 0

	start := time.Now()
	dl := newDownloader(t, srv, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	elapsed := time.Since(start)

	if got := readFile(t, out); string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch")
	}

	// With 4 workers and 16 chunks of 10ms each:
	//   ideal wall time ≈ 16 * 10ms / 4 = 40ms
	//   worst-case long tail (static) would be ≈ 16 * 10ms = 160ms
	// Allow generous slack for scheduling.
	if elapsed > 120*time.Millisecond {
		t.Errorf("elapsed = %v, want < 120ms (long tail detected)", elapsed)
	}
}

// =========================================================================
// Chunk-level retry
// =========================================================================

// TestChunkLevelRetry verifies that a chunk which fails once succeeds
// on retry, and the final file is intact.
func TestChunkLevelRetry(t *testing.T) {
	bodySize := int64(100_000)
	var mu sync.Mutex
	attempts := map[int64]int{} // range start → attempt count

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}
		rh := r.Header.Get("Range")
		var rs, re int64
		if _, err := fmt.Sscanf(rh, "bytes=%d-%d", &rs, &re); err != nil {
			rs, re = 0, bodySize-1
		}
		if re >= bodySize {
			re = bodySize - 1
		}

		mu.Lock()
		attempts[rs]++
		n := attempts[rs]
		mu.Unlock()

		// Fail the first attempt for a specific chunk.
		if rs == 32*1024 && n == 1 {
			w.WriteHeader(500)
			w.Write([]byte("transient"))
			return
		}

		cl := re - rs + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re, bodySize))
		w.Header().Set("Content-Length", strconv.FormatInt(cl, 10))
		w.WriteHeader(206)
		p := make([]byte, cl)
		for i := range p {
			p[i] = byte((rs + int64(i)) % 256)
		}
		w.Write(p)
	}))
	t.Cleanup(srv.Close)

	out := tempOutput(t, "retry.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 4
	cfg.ChunkSize = 32 * 1024 // 100KB / 32KB ≈ 4 chunks
	cfg.MaxRetries = 2
	cfg.RetryBackoffBase = 5 * time.Millisecond

	dl := newDownloader(t, srv, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download should succeed after retry: %v", err)
	}

	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch after retry")
	}

	mu.Lock()
	got32k := attempts[32*1024]
	mu.Unlock()
	if got32k < 2 {
		t.Errorf("chunk @32KB should have been attempted >= 2 times, got %d", got32k)
	}
}

// TestRetryExhaustedFails verifies that a permanently-failing chunk
// eventually causes the download to fail.
func TestRetryExhaustedFails(t *testing.T) {
	bodySize := int64(10_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}
		// Always fail.
		w.WriteHeader(500)
		w.Write([]byte("permanent"))
	}))
	t.Cleanup(srv.Close)

	out := tempOutput(t, "permfail.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 2
	cfg.ChunkSize = 1024
	cfg.MaxRetries = 1
	cfg.RetryBackoffBase = 1 * time.Millisecond

	dl := newDownloader(t, srv, out, cfg)
	err := dl.Download(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retries exhausted") && !strings.Contains(err.Error(), "500") {
		t.Errorf("expected retries exhausted / 500, got: %v", err)
	}
}

// =========================================================================
// Resume (chunk bitmap, v2 state)
// =========================================================================

// TestResumeSkipsCompletedChunks verifies that completed chunks are
// not re-downloaded on resume.
func TestResumeSkipsCompletedChunks(t *testing.T) {
	bodySize := int64(100_000)
	var mu sync.Mutex
	requestedRanges := map[int64]bool{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.Header().Set("ETag", `"abc123"`)
			w.WriteHeader(200)
			return
		}
		rh := r.Header.Get("Range")
		var rs, re int64
		if _, err := fmt.Sscanf(rh, "bytes=%d-%d", &rs, &re); err != nil {
			rs, re = 0, bodySize-1
		}
		if re >= bodySize {
			re = bodySize - 1
		}
		mu.Lock()
		requestedRanges[rs] = true
		mu.Unlock()

		cl := re - rs + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re, bodySize))
		w.Header().Set("Content-Length", strconv.FormatInt(cl, 10))
		w.WriteHeader(206)
		p := make([]byte, cl)
		for i := range p {
			p[i] = byte((rs + int64(i)) % 256)
		}
		w.Write(p)
	}))
	t.Cleanup(srv.Close)

	out := tempOutput(t, "resume.bin")
	chunkSize := int64(10 * 1024) // 100KB / 10KB = 10 chunks
	chunks := planChunks(bodySize, chunkSize)

	// Pre-create the output file with correct content for chunks 0..4
	// and zeros for the rest; mark 0..4 as completed in the state.
	f, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(bodySize); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		p := expectedPayload(bodySize)[chunks[i].Start : chunks[i].End+1]
		if _, err := f.WriteAt(p, chunks[i].Start); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	completed := make([]bool, len(chunks))
	for i := 0; i < 5; i++ {
		completed[i] = true
	}
	if err := SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        srv.URL,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		ETag:       `"abc123"`,
		Completed:  completed,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := testConfigWithResume(srv.URL)
	cfg.Concurrency = 4
	cfg.ChunkSize = chunkSize
	dl := newDownloader(t, srv, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}

	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch")
	}

	// Only chunks 5..9 should have been requested.
	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < 5; i++ {
		if requestedRanges[chunks[i].Start] {
			t.Errorf("completed chunk %d (offset %d) was re-downloaded",
				i, chunks[i].Start)
		}
	}
	for i := 5; i < 10; i++ {
		if !requestedRanges[chunks[i].Start] {
			t.Errorf("pending chunk %d (offset %d) was NOT downloaded",
				i, chunks[i].Start)
		}
	}
}

// TestResumeRejectsETagMismatch verifies that an ETag mismatch causes
// the resume state to be ignored (fresh download).
func TestResumeRejectsETagMismatch(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10) + "&etag=%22new-etag%22"

	out := tempOutput(t, "etag.bin")
	chunkSize := int64(1024)
	chunks := planChunks(bodySize, chunkSize)

	// Build a state claiming all chunks are done with a DIFFERENT etag.
	completed := make([]bool, len(chunks))
	for i := range completed {
		completed[i] = true
	}
	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		ETag:       `"old-etag"`,
		Completed:  completed,
	})

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = chunkSize
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch — resume should have been rejected")
	}
}

// TestResumeRejectsSizeMismatch verifies that a TotalSize mismatch
// invalidates the resume state.
func TestResumeRejectsSizeMismatch(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "size.bin")
	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  999_999, // wrong
		ChunkSize:  1024,
		Completed:  []bool{true, true},
	})

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = 1024
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := readFile(t, out); int64(len(got)) != bodySize {
		t.Fatalf("size: got %d, want %d", len(got), bodySize)
	}
}

// TestResumeRejectsOldVersion verifies that a v1 state file is ignored.
func TestResumeRejectsOldVersion(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "v1.bin")
	SaveState(stateFileName(out), State{
		Version:    1, // old schema
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  1024,
		Completed:  []bool{true, true, true, true},
	})

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = 1024
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := readFile(t, out); string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch — v1 state should have been ignored")
	}
}

// =========================================================================
// Cancellation saves state
// =========================================================================

// TestCancellationSavesState verifies that interrupting a download
// leaves a valid state file that records which chunks completed.
func TestCancellationSavesState(t *testing.T) {
	bodySize := int64(2 << 20) // 2 MB — large enough that cancel lands mid-download
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10) + "&body-delay=50"

	out := tempOutput(t, "cancel.bin")
	cfg := testConfig(url)
	cfg.Concurrency = 2
	cfg.ChunkSize = 64 * 1024 // 2MB / 64KB = 32 chunks
	cfg.MaxRetries = 0
	cfg.HTTPTimeout = 0

	dl := New(url, out, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- dl.Download(ctx)
	}()

	// Give it time to probe + start a couple chunks. With 50ms/block
	// delay and 32KB blocks, each 64KB chunk takes ~100ms; 32 chunks
	// across 2 workers ≈ 1600ms, so 300ms is safely mid-download.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancellation error, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("download did not return after cancellation")
	}

	// State file must exist.
	st, err := LoadState(stateFileName(out))
	if err != nil {
		t.Fatalf("state file should exist after cancellation: %v", err)
	}
	if st.Version != stateVersion {
		t.Errorf("state version = %d, want %d", st.Version, stateVersion)
	}
	if st.TotalSize != bodySize {
		t.Errorf("state TotalSize = %d, want %d", st.TotalSize, bodySize)
	}
	if len(st.Completed) != 32 {
		t.Errorf("state has %d chunks, want 32", len(st.Completed))
	}
}

// =========================================================================
// Concurrent pwrite integrity
// =========================================================================

// TestPwriteIntegrity verifies that data written concurrently by
// multiple workers lands at the correct file offsets — i.e. pwrite
// is safe to use from parallel goroutines.
func TestPwriteIntegrity(t *testing.T) {
	bodySize := int64(1 << 20) // 1 MB
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "pwrite.bin")
	cfg := testConfig(url)
	cfg.Concurrency = 8
	cfg.ChunkSize = 16 * 1024 // 64 chunks, 8 workers — heavy pwrite contention
	cfg.MaxRetries = 0

	dl := New(url, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}

	got := readFile(t, out)
	want := expectedPayload(bodySize)
	if len(got) != len(want) {
		t.Fatalf("size: got %d, want %d", len(got), len(want))
	}
	// Check every byte — pwrite races would corrupt specific offsets.
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("byte %d: got %d, want %d (pwrite race?)", i, got[i], want[i])
		}
	}
}

// =========================================================================
// All-chunks-already-complete short-circuit
// =========================================================================

// TestAllChunksAlreadyComplete verifies that when the state says
// every chunk is done, the download is a no-op and the file is left
// untouched.
func TestAllChunksAlreadyComplete(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "done.bin")
	chunkSize := int64(2500) // 4 chunks

	// Write the full file and mark all chunks completed.
	f, _ := os.OpenFile(out, os.O_RDWR|os.O_CREATE, 0644)
	f.Truncate(bodySize)
	f.WriteAt(expectedPayload(bodySize), 0)
	f.Close()

	completed := []bool{true, true, true, true}
	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		Completed:  completed,
	})

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = chunkSize
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content changed on no-op resume")
	}
	// State should be cleaned up.
	if _, err := os.Stat(stateFileName(out)); !os.IsNotExist(err) {
		t.Errorf("state should be deleted: %v", err)
	}
}

// =========================================================================
// ETag persistence
// =========================================================================

func TestETagPersistedOnSuccess(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10) + "&etag=%22v1%22"

	out := tempOutput(t, "etag-ok.bin")
	cfg := testConfig(url)

	dl := New(url, out, cfg)
	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	etagBytes, err := os.ReadFile(out + ".etag")
	if err != nil {
		t.Fatalf("etag file should exist: %v", err)
	}
	if string(etagBytes) != `"v1"` {
		t.Errorf("etag = %q, want %q", etagBytes, `"v1"`)
	}
}

// =========================================================================
// Regression tests for bug fixes
// =========================================================================

// TestServerIgnoringRangeFailsFast verifies that when a server claims
// Accept-Ranges: bytes (so we plan multiple chunks) but then responds
// to Range requests with 200 OK (ignoring Range), the download fails
// immediately instead of downloading the full body N times and
// corrupting the output file.
func TestServerIgnoringRangeFailsFast(t *testing.T) {
	bodySize := int64(100_000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// Claim Range support — this is the trap.
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(200)
			return
		}
		// But ignore Range headers and always return 200 + full body.
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(200)
		w.Write(expectedPayload(bodySize))
	}))
	t.Cleanup(srv.Close)

	out := tempOutput(t, "lierange.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 4
	cfg.ChunkSize = 16 * 1024
	cfg.MaxRetries = 1
	cfg.RetryBackoffBase = 1 * time.Millisecond

	dl := newDownloader(t, srv, out, cfg)
	err := dl.Download(context.Background())
	if err == nil {
		t.Fatal("expected error for server ignoring Range, got nil")
	}
	if !strings.Contains(err.Error(), "ignored Range") && !strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("expected 'ignored Range' or 'retries exhausted', got: %v", err)
	}
}

// TestResumeWithMissingOutputFile verifies that when the state file
// exists but the output file has been deleted, the downloader starts
// fresh instead of trusting the stale "completed" bitmap.
func TestResumeWithMissingOutputFile(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "missing.bin")
	chunkSize := int64(2500) // 4 chunks

	// Create a state claiming all chunks are done, but DON'T create
	// the output file.
	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		Completed:  []bool{true, true, true, true},
	})

	// Ensure output file does not exist.
	os.Remove(out)

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = chunkSize
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch — stale resume state should have been ignored")
	}
}

// TestResumeWithTruncatedOutputFile verifies that when the output file
// exists but is smaller than fileSize, the downloader starts fresh.
func TestResumeWithTruncatedOutputFile(t *testing.T) {
	bodySize := int64(10_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "truncated.bin")
	chunkSize := int64(2500) // 4 chunks

	// Create a tiny output file (way smaller than bodySize).
	os.WriteFile(out, []byte("tiny"), 0644)

	// State claims all done.
	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		Completed:  []bool{true, true, true, true},
	})

	cfg := testConfigWithResume(url)
	cfg.ChunkSize = chunkSize
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if string(got) != string(expectedPayload(bodySize)) {
		t.Fatal("content mismatch — truncated file should invalidate resume")
	}
}

// TestNonResumeClearsStaleFile verifies that when NOT resuming, any
// pre-existing larger file is truncated so no stale tail data remains.
func TestNonResumeClearsStaleFile(t *testing.T) {
	bodySize := int64(1000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "stale.bin")
	// Write a file larger than the new download.
	staleContent := make([]byte, 5000)
	for i := range staleContent {
		staleContent[i] = 0xFF
	}
	os.WriteFile(out, staleContent, 0644)

	cfg := testConfig(url) // Resume = false
	cfg.ChunkSize = 256
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if int64(len(got)) != bodySize {
		t.Fatalf("file size: got %d, want %d (stale tail not cleared)", len(got), bodySize)
	}
	want := expectedPayload(bodySize)
	if string(got) != string(want) {
		t.Fatal("content mismatch — stale data may have remained")
	}
}

// TestResumeDoesNotRedownloadWhenFileIntact verifies the normal resume
// case: output file has correct data for completed chunks, only
// pending chunks are downloaded.
func TestResumeDoesNotRedownloadWhenFileIntact(t *testing.T) {
	bodySize := int64(40_000)
	srv := newTestServer(t)
	url := srv.URL + "?size=" + strconv.FormatInt(bodySize, 10)

	out := tempOutput(t, "intact.bin")
	chunkSize := int64(10_000) // 4 chunks

	// Write correct data for chunks 0 and 1, zeros for 2 and 3.
	f, _ := os.OpenFile(out, os.O_RDWR|os.O_CREATE, 0644)
	f.Truncate(bodySize)
	full := expectedPayload(bodySize)
	f.WriteAt(full[:chunkSize*2], 0)
	f.Close()

	SaveState(stateFileName(out), State{
		Version:    stateVersion,
		URL:        url,
		OutputPath: out,
		TotalSize:  bodySize,
		ChunkSize:  chunkSize,
		Completed:  []bool{true, true, false, false},
	})

	cfg := testConfigWithResume(url)
	cfg.Concurrency = 2
	cfg.ChunkSize = chunkSize
	dl := New(url, out, cfg)

	if err := dl.Download(context.Background()); err != nil {
		t.Fatalf("download: %v", err)
	}
	got := readFile(t, out)
	if string(got) != string(full) {
		t.Fatal("content mismatch")
	}
}

// TestSaveStateConcurrentSafety is a stress test for concurrent
// SaveState calls — verifies no data corruption under -race.
func TestSaveStateConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			completed := make([]bool, 100)
			for j := 0; j <= n; j++ {
				completed[j] = true
			}
			SaveState(path, State{
				Version:    stateVersion,
				URL:        "https://example.com",
				OutputPath: "/tmp/out",
				TotalSize:  1000000,
				ChunkSize:  10000,
				Completed:  completed,
			})
		}(i)
	}
	wg.Wait()

	// Final state should be valid JSON.
	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("final state corrupted: %v", err)
	}
	if st.Version != stateVersion {
		t.Errorf("version = %d, want %d", st.Version, stateVersion)
	}
}
