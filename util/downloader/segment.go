package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const readBufSize = 32 * 1024

type Segment struct {
	Index           int
	Start           int64
	End             int64
	TempPath        string
	DownloadedBytes int64
}

func (s *Segment) SegmentSize() int64 {
	if s.End >= 0 && s.Start >= 0 {
		return s.End - s.Start + 1
	}
	return -1
}

func (s *Segment) Download(ctx context.Context, url string, client *http.Client, headers map[string]string, progCh chan<- Progress, totalSize int64) error {
	rangeLen := s.SegmentSize()
	startByte := s.Start
	var existingSize int64

	if fi, err := os.Stat(s.TempPath); err == nil && fi.Size() > 0 {
		existingSize = fi.Size()
		if rangeLen > 0 && existingSize < rangeLen {
			startByte = s.Start + existingSize
		} else if rangeLen <= 0 {
			startByte = s.Start + existingSize
		}
	}

	var file *os.File
	var err error
	if existingSize > 0 && startByte > s.Start {
		file, err = os.OpenFile(s.TempPath, os.O_WRONLY|os.O_APPEND, 0644)
	} else {
		file, err = os.Create(s.TempPath)
		existingSize = 0
		startByte = s.Start
	}
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	defer file.Close()

	rangeHeader := buildRange(startByte, s.End)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if existingSize > 0 {
			s.DownloadedBytes = existingSize
			emitProgress(progCh, s.Index, existingSize, rangeLen, true)
			return nil
		}
		return fmt.Errorf("416 Range Not Satisfiable for %s", rangeHeader)
	}

	if rangeHeader != "" && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d for range request", resp.StatusCode)
	}

	respTotal := parseRespTotal(resp.Header.Get("Content-Range"), resp.ContentLength, rangeLen)

	totalForSegment := rangeLen
	if totalForSegment <= 0 {
		totalForSegment = totalSize
	}

	buf := make([]byte, readBufSize)
	var written int64
	lastReport := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		nr, er := resp.Body.Read(buf)
		if nr > 0 {
			nw, ew := file.Write(buf[:nr])
			if ew != nil {
				return fmt.Errorf("write: %w", ew)
			}
			if nr != nw {
				return fmt.Errorf("short write: %d/%d", nw, nr)
			}
			written += int64(nw)

			now := time.Now()
			if progCh != nil && (now.Sub(lastReport) > 200*time.Millisecond || written%(1024*1024) < int64(nr)) {
				s.DownloadedBytes = existingSize + written
				emitProgress(progCh, s.Index, s.DownloadedBytes, totalForSegment, false)
				lastReport = now
			}
		}
		if errors.Is(er, io.EOF) {
			break
		}
		if er != nil {
			return fmt.Errorf("read: %w", er)
		}
	}

	s.DownloadedBytes = existingSize + written

	if respTotal > 0 && written != respTotal {
		return fmt.Errorf("size mismatch: expected %d, got %d", respTotal, written)
	}

	emitProgress(progCh, s.Index, s.DownloadedBytes, totalForSegment, true)
	return nil
}

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

func emitProgress(ch chan<- Progress, idx int, downloaded, total int64, done bool) {
	if ch == nil {
		return
	}
	select {
	case ch <- Progress{
		SegmentIndex: int(idx),
		Downloaded:   downloaded,
		Total:        total,
		Done:         done,
	}:
	default:
	}
}

func parseRespTotal(contentRange string, contentLength int64, fallback int64) int64 {
	if contentRange != "" {
		if parts := parseContentRange(contentRange); parts != nil {
			total := parts[1] - parts[0] + 1
			if total > 0 {
				return total
			}
		}
	}
	if contentLength > 0 {
		return contentLength
	}
	return fallback
}

func parseContentRange(header string) []int64 {
	prefix := "bytes "
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
	total := int64(0)
	if totalStr != "*" {
		total, _ = strconv.ParseInt(totalStr, 10, 64)
	}

	if total > 0 && end >= total {
		end = total - 1
	}

	return []int64{start, end, total}
}
