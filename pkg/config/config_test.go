package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/channel"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths.ProjectsRoot == "" {
		t.Errorf("ProjectsRoot empty (regression)")
	}
}

func TestLoad_HistoryRetentionDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.History.RetentionDays != 0 {
		t.Errorf("History.RetentionDays default = %d, want 0", cfg.History.RetentionDays)
	}
}

func TestDefaultPath_DevSuffix(t *testing.T) {
	channel.Set("dev")
	t.Cleanup(func() { channel.Set("dev") })
	got := DefaultPath()
	if !strings.Contains(got, "ccpulse-dev/config.toml") {
		t.Errorf("dev DefaultPath() = %q, want suffix %q", got, "ccpulse-dev/config.toml")
	}
}

func TestDefaultPath_ReleaseNoSuffix(t *testing.T) {
	channel.Set("release")
	t.Cleanup(func() { channel.Set("dev") })
	got := DefaultPath()
	if !strings.Contains(got, "ccpulse/config.toml") {
		t.Errorf("release DefaultPath() = %q, want suffix %q", got, "ccpulse/config.toml")
	}
	if strings.Contains(got, "ccpulse-dev") {
		t.Errorf("release DefaultPath() leaked dev suffix: %q", got)
	}
}

func TestDefaultPath_HonorsXDG(t *testing.T) {
	channel.Set("dev")
	t.Cleanup(func() { channel.Set("dev") })
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	got := DefaultPath()
	want := "/tmp/xdg-config/ccpulse-dev/config.toml"
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoad_CacheDirDefaultDev(t *testing.T) {
	channel.Set("dev")
	t.Cleanup(func() { channel.Set("dev") })
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths.CacheDir != "~/.cache/ccpulse-dev" {
		t.Errorf("dev CacheDir = %q, want %q", cfg.Paths.CacheDir, "~/.cache/ccpulse-dev")
	}
}

func TestLoad_CacheDirDefaultRelease(t *testing.T) {
	channel.Set("release")
	t.Cleanup(func() { channel.Set("dev") })
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths.CacheDir != "~/.cache/ccpulse" {
		t.Errorf("release CacheDir = %q, want %q", cfg.Paths.CacheDir, "~/.cache/ccpulse")
	}
}

func TestLoad_ExplicitCacheDirOverridesChannel(t *testing.T) {
	channel.Set("dev")
	t.Cleanup(func() { channel.Set("dev") })
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[paths]
cache_dir = "/explicit/path"
`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths.CacheDir != "/explicit/path" {
		t.Errorf("explicit CacheDir lost: got %q", cfg.Paths.CacheDir)
	}
}

func TestLoad_MissingFileStillResolvesChannelDefault(t *testing.T) {
	channel.Set("dev")
	t.Cleanup(func() { channel.Set("dev") })
	cfg, err := Load("/nonexistent/config.toml")
	if err == nil {
		t.Fatalf("Load on missing file should return error, got nil")
	}
	if cfg.Paths.CacheDir != "~/.cache/ccpulse-dev" {
		t.Errorf("missing-file CacheDir = %q, want %q (regression: channel-aware fallback must run even on file-read error)", cfg.Paths.CacheDir, "~/.cache/ccpulse-dev")
	}
}

func TestLoad_UIReduceMotion(t *testing.T) {
	cases := []struct {
		name string
		body string // empty body means "call Load(\"\")" to exercise the embedded default
		want bool
	}{
		{"default_load_empty_path", "", false},
		{"missing_ui_section", `[paths]
cache_dir = "/x"
`, false},
		{"explicit_false", `[ui]
reduce_motion = false
`, false},
		{"explicit_true", `[ui]
reduce_motion = true
`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.body != "" {
				dir := t.TempDir()
				path = filepath.Join(dir, "config.toml")
				if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.UI.ReduceMotion; got != tc.want {
				t.Errorf("cfg.UI.ReduceMotion = %v, want %v", got, tc.want)
			}
		})
	}
}
