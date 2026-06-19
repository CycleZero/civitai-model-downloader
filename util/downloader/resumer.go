package downloader

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// stateVersion identifies the resume-state schema. Bumping it causes
// older state files to be ignored (a fresh download starts instead of
// risking corruption from an incompatible layout).
const stateVersion = 2

// State is the on-disk resume state for a download. It records the
// completion status of every chunk so that an interrupted download
// can skip already-finished chunks on restart.
type State struct {
	Version      int     `json:"version"`
	URL          string  `json:"url"`
	OutputPath   string  `json:"output_path"`
	TotalSize    int64   `json:"total_size"`
	ChunkSize    int64   `json:"chunk_size"`
	ETag         string  `json:"etag,omitempty"`
	Completed    []bool  `json:"completed"`
	WrittenBytes int64   `json:"written_bytes"`
}

func stateFileName(outputPath string) string {
	return outputPath + ".dlstate"
}

// SaveState atomically writes state to path via a temp file + rename.
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

// LoadState reads and decodes a state file.
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

// loadResumable loads and validates a resume state for the given
// download. Returns nil (meaning "start fresh") when:
//   - the state file does not exist or is unreadable;
//   - the schema version does not match stateVersion;
//   - URL or absolute output path differ;
//   - TotalSize or ChunkSize differ from the current plan;
//   - the ETag is present on both sides and differs.
//
// ETag mismatch is only fatal when both the saved and the freshly
// probed ETag are non-empty, so a missing ETag on either side never
// blocks an otherwise valid resume.
func loadResumable(url, outputPath string, fileSize, chunkSize int64, etag string) *State {
	sp := stateFileName(outputPath)
	state, err := LoadState(sp)
	if err != nil {
		return nil
	}
	if state.Version != stateVersion {
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
	if state.TotalSize != fileSize || state.ChunkSize != chunkSize {
		return nil
	}
	if state.ETag != "" && etag != "" && state.ETag != etag {
		return nil
	}
	if len(state.Completed) == 0 {
		return nil
	}
	return state
}

// DeleteState removes the resume state file (best-effort).
func DeleteState(outputPath string) error {
	return os.Remove(stateFileName(outputPath))
}
