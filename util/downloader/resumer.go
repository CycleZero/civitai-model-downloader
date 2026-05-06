package downloader

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type State struct {
	URL        string         `json:"url"`
	OutputPath string         `json:"output_path"`
	TotalSize  int64          `json:"total_size"`
	Segments   []SegmentState `json:"segments"`
}

type SegmentState struct {
	Index           int    `json:"index"`
	Start           int64  `json:"start"`
	End             int64  `json:"end"`
	TempPath        string `json:"temp_path"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
}

func stateFileName(outputPath string) string {
	return outputPath + ".dlstate"
}

func SaveState(path string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func findResumableState(url, outputPath string) *State {
	sp := stateFileName(outputPath)
	state, err := LoadState(sp)
	if err != nil {
		return nil
	}
	if state.URL != url {
		return nil
	}
	absOut, _ := filepath.Abs(outputPath)
	absSt, _ := filepath.Abs(state.OutputPath)
	if absOut != absSt {
		return nil
	}
	for i := range state.Segments {
		seg := &state.Segments[i]
		fi, err := os.Stat(seg.TempPath)
		if err != nil {
			return nil
		}
		if fi.Size() > 0 && seg.DownloadedBytes <= 0 {
			seg.DownloadedBytes = fi.Size()
		}
		segSize := seg.End - seg.Start + 1
		if segSize > 0 && seg.DownloadedBytes > segSize {
			return nil
		}
	}
	return state
}

func DeleteState(outputPath string) error {
	return os.Remove(stateFileName(outputPath))
}
