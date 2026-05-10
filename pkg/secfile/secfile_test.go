package secfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

func TestMkdirAll_Fresh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "child")
	if err := secfile.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode: got %o want %o", got, want)
	}
}

func TestMkdirAll_TightensExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "child")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed MkdirAll: %v", err)
	}
	if err := secfile.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode: got %o want %o", got, want)
	}
}

func TestWriteFile_Fresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := secfile.WriteFile(path, []byte("hi")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}

func TestWriteFile_TightensExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	if err := secfile.WriteFile(path, []byte("new")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}

func TestOpenFile_Fresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	f, err := secfile.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}

func TestOpenFile_TightensExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	f, err := secfile.OpenFile(path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}
