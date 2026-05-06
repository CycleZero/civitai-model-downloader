package util

import (
	"fmt"
	"os"
	"path/filepath"
)

func CreateFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return os.Create(path)
}

func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func PreallocateFile(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	if err := f.Truncate(size); err != nil {
		if _, err := f.Seek(size-1, 0); err != nil {
			return err
		}
		if _, err := f.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := f.Seek(0, 0); err != nil {
			return err
		}
	}
	return nil
}
