package downloader

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const readBufSize = 32 * 1024

// Chunk is a byte-range work unit for the dynamic worker pool.
// Start and End are inclusive byte offsets within the remote file.
// End < 0 signals an unbounded chunk (used when the server does not
// support Range requests or the file size is unknown).
type Chunk struct {
	Index int
	Start int64
	End   int64
}

// Size returns the byte length of the chunk, or -1 if unbounded.
func (c Chunk) Size() int64 {
	if c.Start < 0 || c.End < 0 {
		return -1
	}
	return c.End - c.Start + 1
}

// planChunks splits a file of size fileSize into chunks of at most
// chunkSize bytes. The last chunk may be shorter.
//
// fileSize <= 0  -> single unbounded chunk (streaming fallback).
// chunkSize <= 0 -> single chunk spanning the whole file.
func planChunks(fileSize, chunkSize int64) []Chunk {
	if fileSize <= 0 {
		return []Chunk{{Index: 0, Start: 0, End: -1}}
	}
	if chunkSize <= 0 || chunkSize >= fileSize {
		return []Chunk{{Index: 0, Start: 0, End: fileSize - 1}}
	}

	n := (fileSize + chunkSize - 1) / chunkSize
	chunks := make([]Chunk, n)
	var start int64
	for i := int64(0); i < n; i++ {
		end := start + chunkSize - 1
		if end > fileSize-1 {
			end = fileSize - 1
		}
		chunks[i] = Chunk{Index: int(i), Start: start, End: end}
		start = end + 1
	}
	return chunks
}

// autoChunkSize selects a reasonable chunk size for the given file
// size and target concurrency. The goal is to keep enough chunks
// per worker (~4x) to allow fast workers to pick up extra work,
// while avoiding excessive per-request overhead on small files.
func autoChunkSize(fileSize int64, concurrency int) int64 {
	const (
		smallFile   = 32 << 20  // 32 MB
		midFile     = 256 << 20 // 256 MB
		defaultSize = 16 << 20  // 16 MB
		minChunk    = 1 << 20   // 1 MB
	)

	if fileSize <= 0 {
		return defaultSize
	}
	if fileSize < smallFile {
		return fileSize
	}

	c := concurrency
	if c < 1 {
		c = 1
	}

	if fileSize < midFile {
		sz := fileSize / int64(c*4)
		if sz < minChunk {
			sz = minChunk
		}
		return sz
	}
	return defaultSize
}

// buildRange constructs an HTTP Range header value. Returns "" when
// both bounds are negative (no range constraint).
func buildRange(start, end int64) string {
	if start < 0 && end < 0 {
		return ""
	}
	if end < 0 {
		return fmt.Sprintf("bytes=%d-", start)
	}
	if start < 0 {
		return fmt.Sprintf("bytes=-%d", end)
	}
	return fmt.Sprintf("bytes=%d-%d", start, end)
}

// parseContentRange parses a "bytes start-end/total" Content-Range
// header. Returns nil on any parse failure. A "*" total yields 0.
func parseContentRange(header string) []int64 {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return nil
	}
	rest := header[len(prefix):]

	slash := strings.IndexByte(rest, '/')
	dash := strings.IndexByte(rest, '-')
	if dash < 0 || slash < 0 || dash > slash {
		return nil
	}

	start, err := strconv.ParseInt(rest[:dash], 10, 64)
	if err != nil {
		return nil
	}
	end, err := strconv.ParseInt(rest[dash+1:slash], 10, 64)
	if err != nil {
		return nil
	}

	totalStr := rest[slash+1:]
	var total int64
	if totalStr != "*" {
		total, _ = strconv.ParseInt(totalStr, 10, 64)
	}
	if total > 0 && end >= total {
		end = total - 1
	}
	return []int64{start, end, total}
}

// emitProgress sends a Progress update without blocking. Drops the
// update if the channel is full to never stall a worker.
func emitProgress(ch chan<- Progress, idx int, downloaded, total int64, done bool) {
	if ch == nil {
		return
	}
	select {
	case ch <- Progress{SegmentIndex: idx, Downloaded: downloaded, Total: total, Done: done}:
	default:
	}
}

// parseRespTotal derives the expected response body length from
// Content-Range, Content-Length, or a fallback.
func parseRespTotal(contentRange string, contentLength int64, fallback int64) int64 {
	if contentRange != "" {
		if parts := parseContentRange(contentRange); parts != nil {
			tot := parts[1] - parts[0] + 1
			if tot > 0 {
				return tot
			}
		}
	}
	if contentLength > 0 {
		return contentLength
	}
	return fallback
}

// reportInterval caps progress emission frequency to avoid flooding.
var reportInterval = 200 * time.Millisecond
