package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "test.txt")
	f, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !FileExists(p) {
		t.Fatal("file should exist")
	}
}

func TestFileExists(t *testing.T) {
	if FileExists("/nonexistent/path/12345") {
		t.Fatal("should not exist")
	}
}

func TestPreallocateFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "prealloc.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := PreallocateFile(f, 4096); err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	if fi.Size() != 4096 {
		t.Fatalf("expected 4096, got %d", fi.Size())
	}
}
