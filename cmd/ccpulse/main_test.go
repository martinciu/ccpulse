package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	got := versionString()
	want := "ccpulse dev (channel dev)"
	if got != want {
		t.Fatalf("versionString() = %q, want %q", got, want)
	}
}

func TestRootCommandRegistersSubcommands(t *testing.T) {
	root := newRootCmd()
	want := []string{"status", "index", "config", "doctor", "version"}
	for _, name := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd == root {
			t.Errorf("subcommand %q not registered", name)
		}
	}
}

func TestEnsureConfigFile_Fresh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ccpulse")
	path := filepath.Join(dir, "config.toml")
	if err := ensureConfigFile(path); err != nil {
		t.Fatalf("ensureConfigFile: %v", err)
	}
	if got, _ := os.Stat(dir); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(path); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

func TestEnsureConfigFile_TightensExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ccpulse")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("# old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := ensureConfigFile(path); err != nil {
		t.Fatalf("ensureConfigFile: %v", err)
	}
	if got, _ := os.Stat(dir); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(path); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

func TestInitDevlog_WarnsOnError(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	// Restore perms in cleanup so t.TempDir's own RemoveAll can recurse.
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	cacheDir := filepath.Join(parent, "denied")
	var buf bytes.Buffer
	closer := initDevlog(true, cacheDir, &buf)
	if closer != nil {
		closer.Close()
	}

	out := buf.String()
	if !strings.Contains(out, "devlog init failed") {
		t.Errorf("missing failure prefix in %q", out)
	}
	if !strings.Contains(out, "debug log disabled") {
		t.Errorf("missing remediation hint in %q", out)
	}
	if !strings.Contains(out, cacheDir) {
		t.Errorf("missing cacheDir path in %q", out)
	}
}

func TestInitDevlog_QuietOnSuccess(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	closer := initDevlog(true, t.TempDir(), &buf)
	if closer != nil {
		defer closer.Close()
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected stderr output on success: %q", buf.String())
	}
}

func TestInitDevlog_ReleaseQuiet(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	closer := initDevlog(false, t.TempDir(), &buf)
	if closer != nil {
		t.Errorf("release Init should return nil closer, got %T", closer)
	}
	if buf.Len() != 0 {
		t.Errorf("release should not write to w: %q", buf.String())
	}
}
