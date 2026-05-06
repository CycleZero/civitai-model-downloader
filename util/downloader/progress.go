// Package downloader provides a multi-threaded, resumable HTTP file downloader
// with progress tracking and graceful cancellation support.
package downloader

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Progress represents the current state of a segment download.
type Progress struct {
	SegmentIndex int
	Downloaded   int64
	Total        int64
	Done         bool
	Error        error
}

// ProgressBar collects progress updates from all segments and renders
// a real-time terminal display showing overall progress, transfer speed,
// and per-segment status.
type ProgressBar struct {
	mu            sync.Mutex
	segments      []*segmentStatus
	totalSize     int64
	downloaded    atomic.Int64
	startTime     time.Time
	lastBytes     int64
	lastTime      time.Time
	speed         float64
	stopped       bool
	segmentsDone  atomic.Int32
	totalSegments int
	lastDrawLen   int
}

type segmentStatus struct {
	index      int
	downloaded int64
	total      int64
	done       bool
	err        error
}

// NewProgressBar creates a new ProgressBar for the given number of segments
// and total download size.
func NewProgressBar(segments int, totalSize int64) *ProgressBar {
	pb := &ProgressBar{
		segments:      make([]*segmentStatus, segments),
		totalSize:     totalSize,
		startTime:     time.Now(),
		lastTime:      time.Now(),
		totalSegments: segments,
	}
	for i := 0; i < segments; i++ {
		pb.segments[i] = &segmentStatus{index: i}
	}
	return pb
}

// Update receives a Progress update from a segment and refreshes the display.
func (pb *ProgressBar) Update(p Progress) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	if pb.stopped {
		return
	}

	segIdx := p.SegmentIndex
	if segIdx >= 0 && segIdx < len(pb.segments) {
		pb.segments[segIdx].downloaded = p.Downloaded
		pb.segments[segIdx].total = p.Total
		if p.Done {
			if !pb.segments[segIdx].done {
				pb.segmentsDone.Add(1)
			}
			pb.segments[segIdx].done = true
		}
		if p.Error != nil {
			pb.segments[segIdx].err = p.Error
		}
	}

	var currentBytes int64
	for _, s := range pb.segments {
		currentBytes += s.downloaded
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

// AddDownloaded atomically adds to the total downloaded byte count.
func (pb *ProgressBar) AddDownloaded(n int64) {
	pb.downloaded.Add(n)
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
	// Print a newline to finalize the progress line.
	fmt.Fprintln(os.Stderr)
}

// render draws the current progress to stderr using carriage return.
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

	doneCount := pb.segmentsDone.Load()

	line := fmt.Sprintf("\r  %s %5.1f%%%s %s%s [%d/%d]",
		bar, pct, sizeStr, speedStr, etaStr, doneCount, pb.totalSegments)

	clearLen := pb.lastDrawLen - len([]rune(line))
	if clearLen > 0 {
		line += strings.Repeat(" ", clearLen)
	}
	pb.lastDrawLen = len([]rune(line))

	fmt.Fprint(os.Stderr, line)
}

// progressBarString returns a simple text-based progress bar.
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

// bytesHuman converts bytes to a human-readable string.
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
