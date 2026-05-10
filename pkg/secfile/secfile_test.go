package secfile_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestWriteFileAtomic_Fresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := secfile.WriteFileAtomic(path, []byte("hello")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("contents = %q, want %q", got, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if g, w := info.Mode().Perm(), os.FileMode(0o600); g != w {
		t.Errorf("file mode: got %o want %o", g, w)
	}
}

func TestWriteFileAtomic_TightensExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	if err := secfile.WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if g, w := info.Mode().Perm(), os.FileMode(0o600); g != w {
		t.Errorf("file mode: got %o want %o", g, w)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("contents = %q, want %q", got, "new")
	}
}

func TestWriteFileAtomic_NoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := secfile.WriteFileAtomic(path, []byte("hello")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("found leftover tmp: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_Concurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	const N = 16
	payloads := make([][]byte, N)
	for i := range payloads {
		payloads[i] = []byte(strings.Repeat("x", 1024) + fmt.Sprintf("|%d", i))
	}

	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(p []byte) {
			defer wg.Done()
			if err := secfile.WriteFileAtomic(path, p); err != nil {
				t.Errorf("WriteFileAtomic: %v", err)
			}
		}(payloads[i])
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after concurrent writes: %v", err)
	}
	matched := false
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("file bytes matched none of the %d input payloads (len=%d)", N, len(got))
	}
}
