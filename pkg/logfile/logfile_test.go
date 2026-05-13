package logfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRotated_TruncatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Write 5 MB — below threshold, no truncation.
	fiveMB := make([]byte, 5*1024*1024)
	f1 := OpenRotated(logPath)
	if f1 == nil {
		t.Fatalf("first open returned nil")
	}
	f1.Write(fiveMB)
	f1.Close()

	info, _ := os.Stat(logPath)
	if info.Size() != 5*1024*1024 {
		t.Errorf("expected 5 MB, got %d", info.Size())
	}

	// Write 6 MB more — total 11 MB, exceeds MaxBytes (10 MB).
	// OpenRotated removes and reopens; file should be fresh (size 0 before write).
	sixMB := make([]byte, 6*1024*1024)
	f2 := OpenRotated(logPath)
	if f2 == nil {
		t.Fatalf("second open returned nil")
	}
	f2.Write(sixMB)
	f2.Close()

	info, _ = os.Stat(logPath)
	// After reopening at 11 MB threshold, the file was truncated and written with 6 MB.
	if info.Size() != 6*1024*1024 {
		t.Errorf("expected 6 MB after rotation, got %d", info.Size())
	}
}

func TestOpenRotated_ReturnsNilOnError(t *testing.T) {
	// Non-existent directory — OpenRotated returns nil without panicking.
	f := OpenRotated("/nonexistent/path/test.log")
	if f != nil {
		t.Errorf("expected nil for bad path, got %v", f)
	}
}