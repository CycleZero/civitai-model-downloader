// Package downloader provides a multi-threaded, resumable HTTP file
// downloader with dynamic chunk-based work distribution, progress
// tracking, and graceful cancellation support.
package downloader

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Progress represents the current state of a chunk download.
type Progress struct {
	SegmentIndex int
	Downloaded   int64
	Total        int64
	Done         bool
	Error        error
}

// ProgressBar collects progress updates from all workers and renders
// a real-time terminal display showing overall progress, transfer
// speed, and per-chunk completion count.
type ProgressBar struct {
	mu           sync.Mutex
	chunks       []*chunkStatus
	totalSize    int64
	downloaded   atomic.Int64
	startTime    time.Time
	lastBytes    int64
	lastTime     time.Time
	speed        float64
	stopped      bool
	chunksDone   atomic.Int32
	totalChunks  int
	lastDrawLen  int
}

type chunkStatus struct {
	index      int
	downloaded int64
	total      int64
	done       bool
	err        error
}

// NewProgressBar creates a ProgressBar for totalChunks chunks and a
// total download size of totalSize bytes.
func NewProgressBar(totalChunks int, totalSize int64) *ProgressBar {
	pb := &ProgressBar{
		chunks:       make([]*chunkStatus, totalChunks),
		totalSize:    totalSize,
		startTime:    time.Now(),
		lastTime:     time.Now(),
		totalChunks:  totalChunks,
	}
	for i := 0; i < totalChunks; i++ {
		pb.chunks[i] = &chunkStatus{index: i}
	}
	return pb
}

// SetCompleted pre-records already-finished chunks (resume scenario).
// initialBytes is the sum of bytes from chunks already done. This
// prevents the bar from starting at 0% when resuming a partial file.
func (pb *ProgressBar) SetCompleted(initialBytes int64, completedChunks int) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.downloaded.Store(initialBytes)
	pb.lastBytes = initialBytes
	pb.chunksDone.Store(int32(completedChunks))
}

// Update receives a Progress update from a worker and refreshes the
// display. It is safe to call concurrently from multiple workers.
func (pb *ProgressBar) Update(p Progress) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	if pb.stopped {
		return
	}

	idx := p.SegmentIndex
	if idx >= 0 && idx < len(pb.chunks) {
		pb.chunks[idx].downloaded = p.Downloaded
		pb.chunks[idx].total = p.Total
		if p.Done {
			if !pb.chunks[idx].done {
				pb.chunksDone.Add(1)
			}
			pb.chunks[idx].done = true
		}
		if p.Error != nil {
			pb.chunks[idx].err = p.Error
		}
	}

	var currentBytes int64
	for _, c := range pb.chunks {
		if c.done {
			currentBytes += c.total
		} else {
			currentBytes += c.downloaded
		}
	}
	pb.downloaded.Store(currentBytes)

	now := time.Now()
	elapsed := now.Sub(pb.lastTime).Seconds()
	if elapsed >= 0.5 {
		pb.speed = float64(currentBytes-pb.lastBytes) / elapsed
		pb.lastBytes = currentBytes
		pb.lastTime = now
	}

	pb.render()
}

// GetDownloaded returns the total bytes downloaded so far.
func (pb *ProgressBar) GetDownloaded() int64 {
	return pb.downloaded.Load()
}

// Stop finalizes the progress display with a newline.
func (pb *ProgressBar) Stop() {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.stopped = true
	fmt.Fprintln(os.Stderr)
}

func (pb *ProgressBar) render() {
	downloaded := pb.downloaded.Load()

	var pct float64
	if pb.totalSize > 0 {
		pct = float64(downloaded) / float64(pb.totalSize) * 100
	}

	bar := pb.progressBarString(pct, 20)

	var sizeStr string
	if pb.totalSize > 0 {
		sizeStr = fmt.Sprintf(" %s/%s", bytesHuman(float64(downloaded)), bytesHuman(float64(pb.totalSize)))
	}

	var speedStr string
	if pb.speed > 0 {
		speedStr = fmt.Sprintf("%s/s", bytesHuman(pb.speed))
	} else {
		speedStr = "? B/s"
	}

	var etaStr string
	if pb.speed > 0 && pb.totalSize > 0 {
		remaining := pb.totalSize - downloaded
		if remaining > 0 {
			eta := time.Duration(float64(remaining)/pb.speed) * time.Second
			etaStr = fmt.Sprintf(" ETA:%s", eta.Round(time.Second))
		}
	}

	doneCount := pb.chunksDone.Load()

	line := fmt.Sprintf("\r  %s %5.1f%%%s %s%s [%d/%d chunks]",
		bar, pct, sizeStr, speedStr, etaStr, doneCount, pb.totalChunks)

	clearLen := pb.lastDrawLen - len([]rune(line))
	if clearLen > 0 {
		line += strings.Repeat(" ", clearLen)
	}
	pb.lastDrawLen = len([]rune(line))

	fmt.Fprint(os.Stderr, line)
}

func (pb *ProgressBar) progressBarString(pct float64, width int) string {
	filled := int(pct * float64(width) / 100)
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
}

func bytesHuman(b float64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%.0f B", b)
	}
	div, exp := float64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", b/div, "KMGTPE"[exp])
}
