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
	if !strings.Contains(string(got), "devlog_test.go") {
		t.Errorf("debug.log missing caller info: %q", got)
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

func TestInit_DevModes(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	closer, err := Init(true, dir)
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
	closer, err := Init(true, dir)
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
