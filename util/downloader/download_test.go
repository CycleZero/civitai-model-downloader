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
	"testing"
	"time"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test HTTP server — configurable via query parameters
// ---------------------------------------------------------------------------

// newTestServer creates an httptest.Server with query-param-controlled behavior.
//
// Query parameters:
//
//	size=N         Content-Length of response body (default: 1024)
//	status=N       HTTP status code (default: 200)
//	delay=D        initial delay in ms before sending response (default: 0)
//	body-delay=D   delay between each 32KB chunk in ms (default: 0)
//	no-range=1     omit Accept-Ranges header entirely
//	partial=1      respond with 206 Partial Content for range requests
//	fail-after=N   close connection after N bytes streamed
//	etag=S         set ETag header to S
//	filename=S     set Content-Disposition filename
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// --- parse query params ---
		bodySize := int64(1024)
		if v := q.Get("size"); v != "" {
			n, _ := strconv.ParseInt(v, 10, 64)
			if n > 0 {
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
		usePartial := q.Get("partial") == "1"
		failAfter, _ := strconv.ParseInt(q.Get("fail-after"), 10, 64)
		etag := q.Get("etag")
		filename := q.Get("filename")

		// --- initial delay ---
		if initDelay > 0 {
			select {
			case <-time.After(time.Duration(initDelay) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}

		// --- handle HEAD request (probe) ---
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

		// --- error status (before any body) ---
		if statusCode >= 400 {
			w.WriteHeader(statusCode)
			w.Write([]byte("error body"))
			return
		}

		// --- filename ---
		if filename != "" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		}

		// --- range handling ---
		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" && !noRange {
			var rangeStart, rangeEnd int64
			_, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &rangeStart, &rangeEnd)
			if err != nil {
				// try "bytes=N-" format
				_, err = fmt.Sscanf(rangeHeader, "bytes=%d-", &rangeStart)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				rangeEnd = bodySize - 1
			}

			if rangeStart >= bodySize {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if rangeEnd >= bodySize {
				rangeEnd = bodySize - 1
			}

			chunkSize := rangeEnd - rangeStart + 1

			if usePartial || true { // always respond with 206 for range requests
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, rangeEnd, bodySize))
				w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
				if etag != "" {
					w.Header().Set("ETag", etag)
				}
				w.WriteHeader(http.StatusPartialContent)

				// stream the requested range
				payload := make([]byte, chunkSize)
				for i := range payload {
					payload[i] = byte((rangeStart + int64(i)) % 256)
				}
				streamBody(w, payload, bodyDelay, failAfter, r.Context())
				return
			}
		}

		// --- normal response (full body) ---
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

// streamBody writes payload to w with optional chunk delay and early-fail support.
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
			// Simulate connection close by returning early
			return
		}

		end := chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[:end]
		n, err := w.Write(chunk)
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

// testConfig returns a downloader.Config suitable for unit tests.
func testConfig(serverURL string) *Config {
	return &Config{
		Concurrency:      2,
		MaxRetries:       2,
		RetryBackoffBase: 10 * time.Millisecond,
		HTTPTimeout:      0, // no timeout by default
		Resume:           false,
		Logger:           zap.NewNop(),
	}
}

// testConfigWithResume is like testConfig but with Resume enabled.
func testConfigWithResume(serverURL string) *Config {
	cfg := testConfig(serverURL)
	cfg.Resume = true
	return cfg
}

// tempOutput returns a path inside t.TempDir() for a test output file.
func tempOutput(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// tempOutputDir returns a path inside t.TempDir() for a test output file in a subdirectory.
func tempOutputDir(t *testing.T, dir, name string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), dir)
	os.MkdirAll(d, 0755)
	return filepath.Join(d, name)
}

// newDownloader creates a Downloader wired to a test server.
func newDownloader(t *testing.T, srv *httptest.Server, output string, cfg *Config) *Downloader {
	t.Helper()
	return New(srv.URL, output, cfg)
}

// readFileString reads a file and returns its content as string.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	return string(data)
}

// fileSize returns the size of a file, or -1 on error.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// =========================================================================
// BUG 1 REGRESSION TESTS — HTTPTimeout forced to 30s
// =========================================================================

// TestHTTPTimeoutZeroPreserved verifies that HTTPTimeout=0 is NOT overridden
// to the default 30s by norm(). Currently BROKEN: norm() sets 0→30s.
func TestHTTPTimeoutZeroPreserved(t *testing.T) {
	cfg := &Config{
		HTTPTimeout: 0,
	}
	cfg.norm()
	if cfg.HTTPTimeout != 0 {
		t.Fatalf("HTTPTimeout should be 0 (meaning no timeout), got %v", cfg.HTTPTimeout)
	}
}

// TestHTTPTimeoutNegativeUsesDefault verifies that a negative HTTPTimeout
// triggers the default (30s). This is the new opt-in mechanism.
func TestHTTPTimeoutNegativeUsesDefault(t *testing.T) {
	cfg := &Config{
		HTTPTimeout: -1,
	}
	cfg.norm()
	if cfg.HTTPTimeout != 30*time.Second {
		t.Fatalf("HTTPTimeout should default to 30s when negative, got %v", cfg.HTTPTimeout)
	}
}

// TestDownloaderClientHasNoGlobalTimeout verifies that when HTTPTimeout=0,
// the underlying http.Client has no global timeout (client.Timeout == 0).
// Currently BROKEN: client.Timeout is set to 30s.
func TestDownloaderClientHasNoGlobalTimeout(t *testing.T) {
	srv := newTestServer(t)
	output := tempOutput(t, "test.bin")
	cfg := testConfig(srv.URL)
	cfg.HTTPTimeout = 0
	dl := newDownloader(t, srv, output, cfg)

	if dl.client.Timeout != 0 {
		t.Fatalf("client.Timeout should be 0 when HTTPTimeout=0, got %v", dl.client.Timeout)
	}
}

// TestExplicitHTTPTimeoutApplied verifies that a positive HTTPTimeout is
// applied to the http.Client.
func TestExplicitHTTPTimeoutApplied(t *testing.T) {
	srv := newTestServer(t)
	output := tempOutput(t, "test.bin")
	cfg := testConfig(srv.URL)
	cfg.HTTPTimeout = 5 * time.Second
	dl := newDownloader(t, srv, output, cfg)

	if dl.client.Timeout != 5*time.Second {
		t.Fatalf("client.Timeout should be 5s, got %v", dl.client.Timeout)
	}
}

// =========================================================================
// BUG 2 REGRESSION TEST — Real error swallowed by "context canceled"
// =========================================================================

// countFailServer returns a test server that returns HTTP 500 for the first N
// requests, then behaves normally for subsequent requests.
func countFailServer(t *testing.T, failCount int, bodySize int64) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var count int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := count
		count++
		mu.Unlock()

		if n < failCount {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("simulated failure"))
			return
		}

		// Normal response
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(http.StatusOK)
		payload := make([]byte, bodySize)
		for i := range payload {
			payload[i] = byte(i % 256)
		}
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRealSegmentErrorNotMaskedByContextCanceled verifies that when a segment
// fails with a real error (e.g., HTTP 500), the downloader returns that error
// — NOT "context canceled". Currently BROKEN: the cascade-cancel+select-race
// often returns "context canceled" instead of the real error.
//
// Design: server returns 500 for the first request (seg0 fails fast), then
// serves normally for subsequent requests. With Concurrency=2, seg0 fails,
// cancel() kills seg1 mid-download. The race between resultCh and ctx.Done()
// sometimes produces "context canceled" — this test catches that.
func TestRealSegmentErrorNotMaskedByContextCanceled(t *testing.T) {
	bodySize := int64(500000) // 500KB
	srv := countFailServer(t, 1, bodySize)

	output := tempOutput(t, "test.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 2
	cfg.MaxRetries = 0 // no retry — fail immediately
	cfg.HTTPTimeout = 0

	dl := newDownloader(t, srv, output, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	err := dl.Download(ctx)
	cancel()

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "context canceled") {
		t.Fatalf("BUG: error should NOT be 'context canceled' after fix, got: %v", err)
	}
	if !strings.Contains(errStr, "segment") && !strings.Contains(errStr, "500") {
		t.Fatalf("expected error about segment 500, got: %v", err)
	}
}

// =========================================================================
// BUG 3 REGRESSION TEST — DeadlineExceeded from client timeout not retried
// =========================================================================

// TestDeadlineExceededFromClientTimeoutIsRetryable verifies that when the
// HTTP client timeout fires (DeadlineExceeded), the downloader should retry
// the segment — it's a transient network issue, not a parent-context
// cancellation. Currently BROKEN: errors.Is(DeadlineExceeded) causes
// immediate return without retry.
func TestDeadlineExceededFromClientTimeoutIsRetryable(t *testing.T) {
	// Server that delays past the HTTP timeout on first request,
	// then responds normally on retry.
	var mu sync.Mutex
	var callCount int
	bodySize := int64(10000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(http.StatusOK)
			return
		}

		mu.Lock()
		n := callCount
		callCount++
		mu.Unlock()

		if n == 0 {
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
		}

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(http.StatusOK)
		payload := make([]byte, bodySize)
		w.Write(payload)
	}))
	defer srv.Close()

	output := tempOutput(t, "test.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 1
	cfg.MaxRetries = 2
	cfg.HTTPTimeout = 1 * time.Second // short timeout — will fire on first request
	cfg.RetryBackoffBase = 50 * time.Millisecond

	dl := newDownloader(t, srv, output, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := dl.Download(ctx)
	if err != nil {
		// BUG: currently returns DeadlineExceeded without retry
		t.Fatalf("BUG: download should succeed after retry, got: %v", err)
	}
}

// =========================================================================
// BUG 4 REGRESSION TESTS — findResumableState too strict
// =========================================================================

// TestFindResumableStateSkipsUnstartedSegments verifies that
// findResumableState does NOT return nil when some segments haven't started
// (no temp file + DownloadedBytes=0). Currently BROKEN: os.Stat fail on
// missing temp file causes entire state to be rejected.
func TestFindResumableStateSkipsUnstartedSegments(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "model.safetensors")
	url := "https://example.com/model"

	// Create temp files for segments 0 and 1
	part0 := filepath.Join(dir, ".model.safetensors.part0")
	part1 := filepath.Join(dir, ".model.safetensors.part1")
	// Segments 2 and 3 have NO temp files (unstarted)
	os.WriteFile(part0, make([]byte, 1000), 0644)
	os.WriteFile(part1, make([]byte, 500), 0644)

	state := State{
		URL:        url,
		OutputPath: output,
		TotalSize:  4000,
		Segments: []SegmentState{
			{Index: 0, Start: 0, End: 999, TempPath: part0, DownloadedBytes: 500},
			{Index: 1, Start: 1000, End: 1999, TempPath: part1, DownloadedBytes: 500},
			{Index: 2, Start: 2000, End: 2999, TempPath: filepath.Join(dir, ".model.safetensors.part2"), DownloadedBytes: 0},
			{Index: 3, Start: 3000, End: 3999, TempPath: filepath.Join(dir, ".model.safetensors.part3"), DownloadedBytes: 0},
		},
	}

	sp := stateFileName(output)
	if err := SaveState(sp, state); err != nil {
		t.Fatal(err)
	}

	got := findResumableState(url, output)
	if got == nil {
		t.Fatal("BUG: findResumableState returned nil — unstarted segments with no temp file should be tolerated")
	}

	if got.Segments[0].DownloadedBytes != 1000 {
		t.Errorf("BUG: segment 0 DownloadedBytes should be corrected to actual file size 1000, got %d",
			got.Segments[0].DownloadedBytes)
	}
	if got.Segments[1].DownloadedBytes != 500 {
		t.Errorf("segment 1 DownloadedBytes = %d, want 500", got.Segments[1].DownloadedBytes)
	}
	if got.Segments[2].DownloadedBytes != 0 {
		t.Errorf("segment 2 DownloadedBytes = %d, want 0 (unstarted)", got.Segments[2].DownloadedBytes)
	}
}

// TestFindResumableStateCorrectsFromFileSize verifies that DownloadedBytes
// is always corrected to the actual file size, even when the saved value is
// non-zero but stale. Currently BROKEN: correction only happens when
// DownloadedBytes <= 0.
func TestFindResumableStateCorrectsFromFileSize(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "model.safetensors")
	url := "https://example.com/model"

	// Temp file has 2000 bytes, but state says only 500
	part0 := filepath.Join(dir, ".model.safetensors.part0")
	os.WriteFile(part0, make([]byte, 2000), 0644)

	state := State{
		URL:        url,
		OutputPath: output,
		TotalSize:  4000,
		Segments: []SegmentState{
			{Index: 0, Start: 0, End: 3999, TempPath: part0, DownloadedBytes: 500},
		},
	}

	sp := stateFileName(output)
	if err := SaveState(sp, state); err != nil {
		t.Fatal(err)
	}

	got := findResumableState(url, output)
	if got == nil {
		t.Fatal("findResumableState returned nil")
	}

	// BUG: currently this is 500 (stale), should be 2000 (actual file size)
	if got.Segments[0].DownloadedBytes != 2000 {
		t.Errorf("BUG: DownloadedBytes should be corrected from actual file size (2000), got %d",
			got.Segments[0].DownloadedBytes)
	}
}

// TestFindResumableStateRejectsCorruptState verifies that a segment temp file
// larger than the segment range causes findResumableState to return nil
// (corrupt state — should start fresh).
func TestFindResumableStateRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "model.safetensors")
	url := "https://example.com/model"

	// Segment range is 0-999 (1000 bytes), but file has 2000 bytes — corrupt
	part0 := filepath.Join(dir, ".model.safetensors.part0")
	os.WriteFile(part0, make([]byte, 2000), 0644)

	state := State{
		URL:        url,
		OutputPath: output,
		TotalSize:  4000,
		Segments: []SegmentState{
			{Index: 0, Start: 0, End: 999, TempPath: part0, DownloadedBytes: 2000},
		},
	}

	sp := stateFileName(output)
	if err := SaveState(sp, state); err != nil {
		t.Fatal(err)
	}

	got := findResumableState(url, output)
	if got != nil {
		t.Fatal("should return nil for corrupt state (file > segment range)")
	}
}

// =========================================================================
// BUG 5 REGRESSION TEST — DownloadedBytes stale after cancellation
// =========================================================================

// TestDownloadedBytesAccurateAfterCancellation verifies that after a context
// cancellation mid-download, the segment's DownloadedBytes reflects bytes
// actually written to disk. Currently BROKEN: DownloadedBytes only updates
// during progress emission (every ~200ms or ~1MB), so the final bytes before
// cancellation may not be recorded.
func TestDownloadedBytesAccurateAfterCancellation(t *testing.T) {
	bodySize := int64(500000) // 500KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(bodySize, 10))
		w.WriteHeader(http.StatusOK)

		payload := make([]byte, bodySize)
		for i := range payload {
			payload[i] = byte(i % 256)
		}
		streamBody(w, payload, 50, 0, r.Context())
	}))
	defer srv.Close()

	output := tempOutput(t, "test.bin")
	cfg := testConfig(srv.URL)
	cfg.Concurrency = 1
	cfg.MaxRetries = 0
	cfg.HTTPTimeout = 0

	dl := newDownloader(t, srv, output, cfg)

	// Cancel after probe completes and some data has been written
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- dl.Download(ctx)
	}()

	// With 50ms/chunk delay and 32KB chunks, after 500ms:
	// ~10 chunks = ~320KB written, but total is 500KB so not complete
	time.Sleep(500 * time.Millisecond)
	cancel()

	// Wait for download to finish (should be fast after cancel)
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish after cancellation")
	}

	// Check temp file exists and has data
	tempPath := filepath.Join(filepath.Dir(output), ".test.bin.part0")
	fi, err := os.Stat(tempPath)
	if err != nil {
		t.Fatalf("temp file should exist: %v", err)
	}
	actualSize := fi.Size()
	if actualSize == 0 {
		t.Fatal("temp file should have data after partial download")
	}

	// Check state file's DownloadedBytes matches actual file size
	sp := stateFileName(output)
	state, err := LoadState(sp)
	if err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	savedBytes := state.Segments[0].DownloadedBytes
	if savedBytes <= 0 {
		t.Fatal("BUG: DownloadedBytes in state is 0 — not updated before cancellation")
	}
	if savedBytes < actualSize {
		t.Errorf("BUG: DownloadedBytes (%d) < actual file size (%d) — progress was not saved on every write",
			savedBytes, actualSize)
	}
}
