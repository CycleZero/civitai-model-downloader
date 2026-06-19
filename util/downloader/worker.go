package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"
)

// chunkResult is the outcome of a single chunk download (after all
// retries have been exhausted on failure).
type chunkResult struct {
	Chunk Chunk
	Err   error
	Bytes int64
}

// onChunkDone is invoked by a worker immediately after a chunk
// finishes successfully. Downloader uses it to persist resume state
// and update the completed bitmap without extra locking on its side.
type onChunkDone func(ch Chunk, bytes int64)

// worker pulls Chunks from taskCh, downloads each via a Range request,
// and writes the payload directly to the shared output file at the
// chunk's offset using concurrent-safe WriteAt (pwrite). Failed chunks
// are retried with exponential backoff up to maxRetries times before
// being reported as failed.
type worker struct {
	id             int
	url            string
	file           *os.File
	client         *http.Client
	headers        map[string]string
	logger         *zap.Logger
	maxRetries     int
	backoff        time.Duration
	supportsRange  bool
}

func (w *worker) run(
	ctx context.Context,
	taskCh <-chan Chunk,
	resultCh chan<- chunkResult,
	progCh chan<- Progress,
	totalSize int64,
	onDone onChunkDone,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case ch, ok := <-taskCh:
			if !ok {
				return
			}
			r := w.downloadWithRetry(ctx, ch, progCh, totalSize)
			if r.Err == nil && onDone != nil {
				onDone(ch, r.Bytes)
			}
			// Send without ctx-guard: the collector drains resultCh
			// until it is closed (after all workers exit), so this
			// send cannot deadlock — even on cancellation.
			resultCh <- r
		}
	}
}

// downloadWithRetry attempts a chunk download up to maxRetries+1
// times with exponential backoff. A nil context error is treated as
// retryable; a canceled parent context is returned immediately.
func (w *worker) downloadWithRetry(ctx context.Context, ch Chunk, progCh chan<- Progress, totalSize int64) chunkResult {
	var lastErr error
	for attempt := 0; attempt <= w.maxRetries; attempt++ {
		if attempt > 0 {
			bo := w.backoff * (1 << (attempt - 1))
			if bo > maxBackoff {
				bo = maxBackoff
			}
			select {
			case <-ctx.Done():
				return chunkResult{Chunk: ch, Err: ctx.Err()}
			case <-time.After(bo):
			}
		}

		bytes, err := w.downloadChunk(ctx, ch, progCh, totalSize)
		if err == nil {
			return chunkResult{Chunk: ch, Bytes: bytes}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return chunkResult{Chunk: ch, Err: ctxErr, Bytes: bytes}
		}
		lastErr = err
		w.logger.Sugar().Warnf("chunk %d attempt %d/%d failed: %v",
			ch.Index, attempt+1, w.maxRetries+1, err)
	}
	return chunkResult{Chunk: ch, Err: fmt.Errorf("chunk %d: retries exhausted: %w", ch.Index, lastErr)}
}

// downloadChunk performs a single Range GET for the chunk and writes
// the body to w.file at offset ch.Start using WriteAt (pwrite),
// which is safe to call concurrently from multiple goroutines.
func (w *worker) downloadChunk(ctx context.Context, ch Chunk, progCh chan<- Progress, totalSize int64) (int64, error) {
	rangeHeader := ""
	if w.supportsRange {
		rangeHeader = buildRange(ch.Start, ch.End)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	if err != nil {
		return 0, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	for k, v := range w.headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return 0, fmt.Errorf("416 Range Not Satisfiable for %s", rangeHeader)
	}
	// A Range request answered with 200 OK means the server ignored
	// the Range header and is sending the FULL body. Accepting that
	// would make every worker download the entire file and write it
	// at its own offset — corrupting the output and wasting N×
	// bandwidth. Fail fast so retry/fallback can handle it.
	if rangeHeader != "" && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("server ignored Range request: got status %d, expected 206", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	expected := parseRespTotal(resp.Header.Get("Content-Range"), resp.ContentLength, ch.Size())
	totalForProgress := ch.Size()
	if totalForProgress <= 0 {
		totalForProgress = totalSize
	}

	buf := make([]byte, readBufSize)
	var written int64
	offset := ch.Start
	lastReport := time.Now()

	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		nr, er := resp.Body.Read(buf)
		if nr > 0 {
			nw, ew := w.file.WriteAt(buf[:nr], offset)
			if ew != nil {
				return written, fmt.Errorf("pwrite: %w", ew)
			}
			if nw != nr {
				return written, fmt.Errorf("short pwrite: %d/%d", nw, nr)
			}
			offset += int64(nw)
			written += int64(nw)

			now := time.Now()
			if progCh != nil && now.Sub(lastReport) > reportInterval {
				emitProgress(progCh, ch.Index, written, totalForProgress, false)
				lastReport = now
			}
		}
		if errors.Is(er, io.EOF) {
			break
		}
		if er != nil {
			return written, fmt.Errorf("read: %w", er)
		}
	}

	if expected > 0 && written != expected {
		return written, fmt.Errorf("chunk %d size mismatch: expected %d, got %d", ch.Index, expected, written)
	}

	emitProgress(progCh, ch.Index, written, totalForProgress, true)
	return written, nil
}
