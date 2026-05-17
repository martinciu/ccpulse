package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/martinciu/ccpulse/pkg/devlog"
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
	closer := initDevlog(true, cacheDir, slog.LevelDebug, &buf)
	if closer != nil {
		closer.Close()
	}

	out := buf.String()
	if !strings.Contains(out, "devlog init failed") {
		t.Errorf("missing failure prefix in %q", out)
	}
	if !strings.Contains(out, "log disabled") {
		t.Errorf("missing remediation hint in %q", out)
	}
	if !strings.Contains(out, "check "+cacheDir+" permissions") {
		t.Errorf("missing remediation hint referencing cacheDir in %q", out)
	}
}

func TestInitDevlog_QuietOnSuccess(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	closer := initDevlog(true, t.TempDir(), slog.LevelDebug, &buf)
	if closer != nil {
		defer closer.Close()
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected stderr output on success: %q", buf.String())
	}
}

func TestInitDevlog_LevelOffQuiet(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// At LevelOff, devlog.Init short-circuits: no file opened, nil closer,
	// no stderr output. Matches the pre-#138 release-mode behaviour.
	var buf bytes.Buffer
	closer := initDevlog(false, t.TempDir(), devlog.LevelOff, &buf)
	if closer != nil {
		t.Errorf("LevelOff should return nil closer, got %T", closer)
	}
	if buf.Len() != 0 {
		t.Errorf("LevelOff should not write to w: %q", buf.String())
	}
}

// TestRunTUI_MalformedConfig asserts that runTUI returns a non-nil error
// when config.toml exists but is syntactically invalid (stray bracket).
// Mirrors the runIndex pattern: os.IsNotExist → fine; any other error → fatal.
func TestRunTUI_MalformedConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// channel is "dev" by default in tests; DefaultPath() → $XDG_CONFIG_HOME/ccpulse-dev/config.toml
	ccpulseDir := filepath.Join(cfgDir, "ccpulse-dev")
	if err := os.MkdirAll(ccpulseDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccpulseDir, "config.toml"), []byte("[[broken\n"), 0600); err != nil {
		t.Fatal(err)
	}

	err := runTUI(t.Context(), io.Discard)
	if err == nil {
		t.Fatal("runTUI should return error for malformed config, got nil")
	}
	var perr toml.ParseError
	if !errors.As(err, &perr) {
		t.Errorf("error should unwrap to *toml.ParseError, got: %v", err)
	}
}

// TestLoadConfigOrDefault_AbsentReturnsDefaults asserts the absent-config
// path of the helper: missing file is silent, caller gets a usable cfg.
func TestLoadConfigOrDefault_AbsentReturnsDefaults(t *testing.T) {
	cfg, err := loadConfigOrDefault(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("absent config should be silent, got: %v", err)
	}
	if cfg.Paths.CacheDir == "" {
		t.Error("cfg.Paths.CacheDir should be a sane default, got empty")
	}
}

// TestLoadConfigOrDefault_MalformedReturnsWrappedError asserts a syntax
// error in config.toml surfaces as a wrapped error that includes the path
// and unwraps to toml.ParseError — so callers in cmd/ccpulse/ can rely on
// errors.As without owning the wrap pattern themselves.
func TestLoadConfigOrDefault_MalformedReturnsWrappedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfigOrDefault(path)
	if err == nil {
		t.Fatal("expected error for malformed config, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should include path %q, got: %v", path, err)
	}
	var perr toml.ParseError
	if !errors.As(err, &perr) {
		t.Errorf("error should unwrap to toml.ParseError, got: %v", err)
	}
}
