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

// TestWriteFileAtomic_Concurrent stresses WriteFileAtomic under contention
// from N writers and R readers running simultaneously. Writers each loop,
// repeatedly writing their own distinct 8 KiB payload; readers continuously
// re-read the file and assert it always matches *some* writer's payload.
// A non-atomic write of an 8 KiB payload would tear across multiple syscalls
// on many filesystems and a reader would see partial bytes — catching the
// regression this PR guards against.
//
// Also asserts the 0600 mode invariant holds under contention and that the
// final file equals one of the payloads byte-for-byte.
func TestWriteFileAtomic_Concurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	const (
		N         = 16          // writers
		R         = 8           // readers
		Iters     = 20          // writes per writer
		PayloadSz = 8 * 1024    // larger than typical fs block — defeats single-syscall atomicity
	)

	payloads := make([][]byte, N)
	for i := range payloads {
		marker := fmt.Sprintf("|writer-%02d|", i)
		fill := byte('a' + i%26)
		buf := bytes.Repeat([]byte{fill}, PayloadSz-len(marker))
		payloads[i] = append(buf, marker...)
	}
	payloadSet := make(map[string]struct{}, N)
	for _, p := range payloads {
		payloadSet[string(p)] = struct{}{}
	}

	// Seed so readers always see a valid file from t=0.
	if err := secfile.WriteFileAtomic(path, payloads[0]); err != nil {
		t.Fatalf("seed WriteFileAtomic: %v", err)
	}

	start := make(chan struct{})
	stop := make(chan struct{})

	var writers sync.WaitGroup
	writers.Add(N)
	for i := range N {
		go func(p []byte) {
			defer writers.Done()
			<-start
			for range Iters {
				if err := secfile.WriteFileAtomic(path, p); err != nil {
					t.Errorf("WriteFileAtomic: %v", err)
					return
				}
			}
		}(payloads[i])
	}

	var readers sync.WaitGroup
	readers.Add(R)
	for range R {
		go func() {
			defer readers.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("torn read: ReadFile: %v", err)
					return
				}
				if _, ok := payloadSet[string(got)]; !ok {
					t.Errorf("torn read: %d bytes not in payload set", len(got))
					return
				}
			}
		}()
	}

	close(start) // release all goroutines simultaneously
	writers.Wait()
	close(stop) // readers exit on next loop iteration
	readers.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("final ReadFile: %v", err)
	}
	if _, ok := payloadSet[string(got)]; !ok {
		t.Errorf("final bytes matched none of the %d input payloads (len=%d)", N, len(got))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if g, w := info.Mode().Perm(), os.FileMode(0o600); g != w {
		t.Errorf("file mode under contention: got %o want %o", g, w)
	}
}
