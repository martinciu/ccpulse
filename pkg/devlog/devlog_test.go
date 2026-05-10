package devlog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_DevWritesDebugLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(true, dir)
	if err != nil {
		t.Fatal(err)
	}
	if closer == nil {
		t.Fatal("Init(true) returned nil closer")
	}
	defer closer.Close()

	slog.Debug("hello dev", "key", "value")

	logPath := filepath.Join(dir, "debug.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hello dev") {
		t.Errorf("debug.log missing slog output: %q", got)
	}
}

func TestInit_ReleaseDoesNotCreateDebugLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(false, dir)
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Errorf("Init(false) should return nil closer, got %T", closer)
	}

	slog.Debug("should not appear anywhere")

	if _, err := os.Stat(filepath.Join(dir, "debug.log")); !os.IsNotExist(err) {
		t.Errorf("release Init created debug.log; want absent")
	}
}

func TestInit_DevCreatesParentDir(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := filepath.Join(t.TempDir(), "missing-subdir")
	closer, err := Init(true, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if _, err := os.Stat(filepath.Join(dir, "debug.log")); err != nil {
		t.Errorf("debug.log not created under fresh dir: %v", err)
	}
}
