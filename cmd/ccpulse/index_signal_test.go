package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestIndexSIGINTExitsCleanly builds the ccpulse binary, points it
// at a fake projects tree large enough that `ccpulse index --rebuild`
// takes well over our SIGINT delay, and asserts the process exits
// within 2s of receiving SIGINT (proving signal.NotifyContext +
// ExecuteContext + cmd.Context() flow all the way through to
// Backfill.Run's <-ctx.Done() check).
func TestIndexSIGINTExitsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syscall.SIGINT not supported on Windows; ccpulse is unix-only")
	}

	// Build the binary into a temp dir.
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "ccpulse")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/ccpulse")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	// Build a fake projects tree: 200 files × 100 lines each. JSONL
	// content doesn't have to be valid — Ingester logs and skips bad
	// files, so the walk still has work to do.
	projects := filepath.Join(tmp, "projects")
	mkFakeProjectsTree(t, projects, 200, 100)

	cache := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "index", "--rebuild")
	cmd.Env = append(os.Environ(),
		"CCPULSE_PROJECTS_ROOT="+projects,
		"CCPULSE_CACHE_DIR="+cache,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Send SIGINT after 200ms — long enough that some files have been
	// processed, short enough that the walk hasn't completed.
	time.AfterFunc(200*time.Millisecond, func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
	})

	// Bound the wait so a hung process fails fast instead of timing
	// out the test.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// Expect non-zero exit (we return ctx.Err() from runIndex).
		if err == nil {
			t.Errorf("ccpulse index exited 0 after SIGINT; want non-zero")
		}
		// Sanity: no panic, no go runtime stacktrace in stderr.
		if strings.Contains(stderr.String(), "panic:") ||
			strings.Contains(stderr.String(), "goroutine ") {
			t.Errorf("unexpected panic / stacktrace in stderr:\n%s", stderr.String())
		}
	case <-time.After(2*time.Second + 200*time.Millisecond):
		_ = cmd.Process.Kill()
		t.Fatalf("ccpulse index did not exit within 2s of SIGINT\nstdout:\n%s\nstderr:\n%s",
			stdout.String(), stderr.String())
	}
}

// mkFakeProjectsTree creates `nFiles` JSONL files spread across a
// few subdirectories under root, each with `nLines` of bogus content.
func mkFakeProjectsTree(t *testing.T, root string, nFiles, nLines int) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nFiles; i++ {
		dir := filepath.Join(root, fmt.Sprintf("proj-%03d", i/20))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < nLines; j++ {
			fmt.Fprintf(f, `{"type":"placeholder","i":%d,"j":%d}`+"\n", i, j)
		}
		f.Close()
	}
}

// repoRoot walks up from the test file until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from cwd")
		}
		dir = parent
	}
}
