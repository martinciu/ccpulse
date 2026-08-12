package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"

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
	if err := os.MkdirAll(ccpulseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccpulseDir, "config.toml"), []byte("[[broken\n"), 0o600); err != nil {
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

// runTUIBounded runs runTUI in a goroutine and returns its result, failing
// the test if it does not return within 5s. runTUI cannot be stopped
// externally once the program starts — newTeaProgram never passes
// tea.WithContext, so neither ctx cancellation nor input EOF quits
// bubbletea; only a `q` keypress does. If that keypress is ever dropped, a
// synchronous call would block until the package timeout (10 min by
// default), panic, and fail every other test in the package with a stack
// that doesn't identify the offending assertion (#492).
func runTUIBounded(t *testing.T, ctx context.Context) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runTUI(ctx, io.Discard) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("runTUI did not return within 5s")
	}
	return nil // unreachable: t.Fatal stops the test
}

// TestRunTUI_AbsentConfigUsesDefaults asserts that runTUI proceeds normally
// (doesn't error on the config step) when the config file simply doesn't exist.
// The absent-config path is guarded by os.IsNotExist — defaults kick in.
// We inject a no-op tea.Program so the test exits cleanly via 'q'.
func TestRunTUI_AbsentConfigUsesDefaults(t *testing.T) {
	// Point XDG_CONFIG_HOME at a dir with no ccpulse-dev subdirectory.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-config-here"))
	t.Setenv("CCPULSE_PROJECTS_ROOT", t.TempDir())
	t.Setenv("CCPULSE_CACHE_DIR", t.TempDir())

	originalNewTeaProgram := newTeaProgram
	t.Cleanup(func() { newTeaProgram = originalNewTeaProgram })
	newTeaProgram = func(m tea.Model) *tea.Program {
		return tea.NewProgram(m,
			tea.WithoutRenderer(),
			tea.WithInput(strings.NewReader("q")),
			tea.WithOutput(io.Discard),
		)
	}

	if err := runTUIBounded(t, t.Context()); err != nil {
		t.Fatalf("runTUI should succeed with absent config (defaults), got: %v", err)
	}
}
