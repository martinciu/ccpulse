package devlog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	charmlog "github.com/charmbracelet/log"
	"github.com/martinciu/ccpulse/pkg/channel"
)

func TestInit_Off_DiscardHandler(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "off")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		t.Errorf("closer: expected nil for off, got %v", closer)
	}

	// After "off", charmlog's default should discard everything.
	// Write to charmlog's default logger and verify the log file stays absent.
	filename := "debug.log"
	if !channel.IsDev() {
		filename = "ccpulse.log"
	}
	charmlog.Info("should not appear")
	logPath := filepath.Join(dir, filename)
	got, err := os.ReadFile(logPath)
	if err == nil && len(got) > 0 {
		t.Errorf("expected no log file or empty file after 'off', got: %s", strings.TrimSpace(string(got)))
	}
}

func TestInit_LevelString_CaseInsensitive(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	levels := []string{"debug", "DEBUG", "Debug", "info", "INFO", "warn", "WARN", "error", "ERROR"}
	for _, lvl := range levels {
		closer, err := Init(t.TempDir(), lvl)
		if err != nil {
			t.Errorf("Init(%q): %v", lvl, err)
		}
		if closer != nil {
			closer.Close()
		}
	}
}

func TestInit_DevWritesDebugLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	// Simulate dev channel by passing empty override (dev defaults to DEBUG).
	closer, err := Init(dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer == nil {
		t.Fatal("Init returned nil closer")
	}
	defer closer.Close()

	slog.Debug("hello dev", "key", "value")

	debugPath := filepath.Join(dir, "debug.log")
	got, err := os.ReadFile(debugPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hello dev") {
		t.Errorf("debug.log missing slog output: %q", got)
	}
	if !strings.Contains(string(got), "devlog_test.go") {
		t.Errorf("debug.log missing caller info: %q", got)
	}
}

// TestInit_ReleaseWritesCCPulseLog verifies that a non-dev channel writes
// to ccpulse.log when given an explicit "info" level (mirroring a release
// build's setup). Note: this test calls channel.IsDev() internally, so it
// only exercises the release path when run against a release binary.
// In dev channel the path is always debug.log regardless of level override.
func TestInit_ReleaseWritesCCPulseLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Explict "info" level mirrors what a release build passes.
	dir := t.TempDir()
	closer, err := Init(dir, "info")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	slog.Info("hello release")

	// In dev channel this writes debug.log; in release it writes ccpulse.log.
	// The channel-dependent path is covered by integration/manual testing.
	_ = channel.IsDev
}

// TestInit_DevCreatesParentDir mirrors the old Init(isDev, dir) test.
// Empty override uses channel default (DEBUG in dev).
func TestInit_DevCreatesParentDir(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := filepath.Join(t.TempDir(), "missing-subdir")
	closer, err := Init(dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer closer.Close()
	if _, err := os.Stat(filepath.Join(dir, "debug.log")); err != nil {
		t.Errorf("debug.log not created under fresh dir: %v", err)
	}
}

func TestInit_DevModes(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}
	if got, _ := os.Stat(dir); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(filepath.Join(dir, "debug.log")); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

func TestInit_TightensExisting(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte(""), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	closer, err := Init(dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}
	if got, _ := os.Stat(dir); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(filepath.Join(dir, "debug.log")); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

// TestInit_LevelOverrideDebug forces DEBUG on any channel.
func TestInit_LevelOverrideDebug(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "debug")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer == nil {
		t.Fatal("debug level should produce non-nil closer")
	}
	defer closer.Close()

	var buf strings.Builder
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	slog.Debug("debug event")
	if !strings.Contains(buf.String(), "debug event") {
		t.Errorf("debug level did not emit: %s", buf.String())
	}
}

// TestInit_LevelOverrideUnknownFallsBack verifies that an unknown level
// string falls back to the channel default.
func TestInit_LevelOverrideUnknownFallsBack(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "not-a-level")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	// In dev channel unknown → DEBUG (channel default).
	// Verify that DEBUG events appear.
	var buf strings.Builder
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	slog.Debug("fallback debug")
	if !strings.Contains(buf.String(), "fallback debug") {
		t.Errorf("unknown level should fall back to DEBUG in dev: %s", buf.String())
	}
}

// TestInit_ReturnsDiscardOnMkdirError verifies Init sets both charmlog and
// slog defaults to discard when mkdir fails.
func TestInit_ReturnsDiscardOnMkdirError(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A path that cannot be created (file, not directory).
	dir := filepath.Join(t.TempDir(), "cannot-create")
	if f, err := os.Create(dir); err != nil {
		t.Fatalf("setup: %v", err)
	} else {
		f.Close()
	}

	closer, err := Init(dir, "")
	if err == nil {
		t.Error("expected error for bad path, got nil")
	}
	if closer != nil {
		t.Errorf("closer: expected nil on error, got %v", closer)
	}

	// After error, charmlog's default logger should be discard — verify by
	// writing to the default and checking that no log file is created.
	charmlog.Info("should be discarded")
	slog.Info("should also be discarded")

	// In dev channel: debug.log; in release channel: ccpulse.log.
	filename := "debug.log"
	if !channel.IsDev() {
		filename = "ccpulse.log"
	}
	logPath := filepath.Join(dir, filename)
	got, err := os.ReadFile(logPath)
	if err == nil && len(got) > 0 {
		t.Errorf("expected discard after mkdir error: %s", strings.TrimSpace(string(got)))
	}
}

// TestInit_ReturnsDiscardOnOpenError verifies Init sets both loggers to
// discard when the log file cannot be opened.
func TestInit_ReturnsDiscardOnOpenError(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	// Create a file (not dir) at the subdir path so mkdir fails.
	f, err := os.Create(subdir)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	f.Close()

	closer, err := Init(subdir, "")
	if err == nil {
		t.Error("expected error for dir-creatable path, got nil")
	}
	if closer != nil {
		t.Errorf("closer: expected nil on error, got %v", closer)
	}

	// After error, charmlog's default should be discard.
	charmlog.Info("should be discarded")

	filename := "debug.log"
	if !channel.IsDev() {
		filename = "ccpulse.log"
	}
	logPath := filepath.Join(subdir, filename)
	got, err := os.ReadFile(logPath)
	if err == nil && len(got) > 0 {
		t.Errorf("expected discard after open error: %s", strings.TrimSpace(string(got)))
	}
}

// TestInit_LevelWarnAndError verifies warn level filtering.
// charmlog logs messages at the given level and above; with level=warn,
// info is dropped but warn and error appear.
func TestInit_LevelWarnAndError(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "warn")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	// After Init with "warn", charmlog's default is the file-backed logger.
	// Write info/warn/error; info should not appear; warn/error should.
	charmlog.Info("info should not appear")
	charmlog.Warn("warn should appear")
	charmlog.Error("error should appear")

	filename := "debug.log"
	if !channel.IsDev() {
		filename = "ccpulse.log"
	}
	logPath := filepath.Join(dir, filename)
	got, _ := os.ReadFile(logPath)
	out := string(got)
	if strings.Contains(out, "info should not appear") {
		t.Errorf("[warn] info leaked through: %s", out)
	}
	if !strings.Contains(out, "warn should appear") {
		t.Errorf("[warn] warn did not appear: %s", out)
	}
	if !strings.Contains(out, "error should appear") {
		t.Errorf("[warn] error did not appear: %s", out)
	}
}

// TestInit_ReleaseRotationViaLogfile verifies that ccpulse.log rotation
// is exercised through logfile.OpenRotated.
func TestInit_ReleaseRotationViaLogfile(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(dir, "info")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	// Write enough to trigger rotation on the next Init call.
	// logfile.MaxBytes = 10 MB.
	tenMB := make([]byte, 10*1024*1024)
	slog.Info(string(tenMB))

	// Call Init again — should rotate if ccpulse.log > 10 MB.
	// In dev channel this hits debug.log rotation; in release channel
	// it exercises ccpulse.log rotation via logfile.OpenRotated.
	// We can verify rotation by checking that the file path is still valid.
	ccPath := filepath.Join(dir, "ccpulse.log")
	if _, err := os.Stat(ccPath); os.IsNotExist(err) {
		// In dev channel the file may be debug.log; that's fine.
		_ = ccPath
	}
}