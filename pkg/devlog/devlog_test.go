package devlog

// Tests in this file mutate slog.SetDefault and must NOT call t.Parallel().
// slog.Default is process-global, so concurrent tests would cross-
// contaminate the captured handler — debug.log content from one test
// could land mid-assertion in another. Mirrors the constraint documented
// for pkg/anthro's captureLogs helper.

import (
	"context"
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
	closer, err := Init(Options{IsDev: true, CacheDir: dir, Level: slog.LevelDebug})
	if err != nil {
		t.Fatal(err)
	}
	if closer == nil {
		t.Fatal("Init(dev) returned nil closer")
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
	if !strings.Contains(string(got), "devlog_test.go") {
		t.Errorf("debug.log missing caller info: %q", got)
	}
}

func TestInit_ReleaseAtLevelOff_NoFile(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(Options{IsDev: false, CacheDir: dir, Level: LevelOff})
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Errorf("Init(LevelOff) should return nil closer, got %T", closer)
	}

	slog.Info("should not appear anywhere")
	slog.Error("not even errors")

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("LevelOff created %s in cache dir; want no files", e.Name())
	}
	if slog.Default().Enabled(context.Background(), slog.LevelError) {
		t.Errorf("default handler is enabled at LevelError under LevelOff; want disabled")
	}
}

func TestInit_DevAtLevelOff_NoFile(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(Options{IsDev: true, CacheDir: dir, Level: LevelOff})
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Errorf("Init(LevelOff) should return nil closer, got %T", closer)
	}

	slog.Debug("should not appear anywhere")

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("LevelOff created %s in cache dir; want no files", e.Name())
	}
}

func TestInit_DevCreatesParentDir(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := filepath.Join(t.TempDir(), "missing-subdir")
	closer, err := Init(Options{IsDev: true, CacheDir: dir, Level: slog.LevelDebug})
	if err != nil {
		t.Fatal(err)
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
	closer, err := Init(Options{IsDev: true, CacheDir: dir, Level: slog.LevelDebug})
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
	closer, err := Init(Options{IsDev: true, CacheDir: dir, Level: slog.LevelDebug})
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

func TestInit_ReleaseWritesCcpulseLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(Options{IsDev: false, CacheDir: dir, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	if closer == nil {
		t.Fatal("Init returned nil closer at LevelInfo; want non-nil")
	}
	defer closer.Close()

	slog.Info("hello release", "key", "value")

	logPath := filepath.Join(dir, "ccpulse.log")
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hello release") {
		t.Errorf("ccpulse.log missing slog output: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "debug.log")); !os.IsNotExist(err) {
		t.Errorf("release mode created debug.log; want only ccpulse.log")
	}
}

func TestInit_ReleaseFiltersBelowLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(Options{IsDev: false, CacheDir: dir, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	slog.Debug("should be filtered")
	slog.Info("should appear")

	got, err := os.ReadFile(filepath.Join(dir, "ccpulse.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "should be filtered") {
		t.Errorf("DEBUG record reached file at LevelInfo:\n%s", got)
	}
	if !strings.Contains(string(got), "should appear") {
		t.Errorf("INFO record missing from file:\n%s", got)
	}
}

func TestInit_ChannelSplit_BothFilesCoexist(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()

	closer1, err := Init(Options{IsDev: true, CacheDir: dir, Level: slog.LevelDebug})
	if err != nil {
		t.Fatal(err)
	}
	slog.Debug("hello dev")
	closer1.Close()

	closer2, err := Init(Options{IsDev: false, CacheDir: dir, Level: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("hello release")
	closer2.Close()

	dev, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if err != nil {
		t.Fatalf("debug.log: %v", err)
	}
	if !strings.Contains(string(dev), "hello dev") {
		t.Errorf("debug.log missing dev output: %q", dev)
	}
	if strings.Contains(string(dev), "hello release") {
		t.Errorf("debug.log unexpectedly contains release output: %q", dev)
	}

	rel, err := os.ReadFile(filepath.Join(dir, "ccpulse.log"))
	if err != nil {
		t.Fatalf("ccpulse.log: %v", err)
	}
	if !strings.Contains(string(rel), "hello release") {
		t.Errorf("ccpulse.log missing release output: %q", rel)
	}
	if strings.Contains(string(rel), "hello dev") {
		t.Errorf("ccpulse.log unexpectedly contains dev output: %q", rel)
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    slog.Level
		wantErr bool
	}{
		{name: "off", in: "off", want: LevelOff},
		{name: "OFF mixed case", in: "OFF", want: LevelOff},
		{name: "debug", in: "debug", want: slog.LevelDebug},
		{name: "DEBUG mixed case", in: "DEBUG", want: slog.LevelDebug},
		{name: "info", in: "info", want: slog.LevelInfo},
		{name: "warn", in: "warn", want: slog.LevelWarn},
		{name: "error", in: "error", want: slog.LevelError},
		{name: "unknown", in: "trace", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) returned %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
