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
	if cfg.Display.Mode != "auto" {
		t.Errorf("Display.Mode default = %q, want auto", cfg.Display.Mode)
	}
	if cfg.Display.CostWarnUSD == 0 || cfg.Display.CostHotUSD == 0 {
		t.Errorf("Display cost thresholds zero: %+v", cfg.Display)
	}
	if cfg.Paths.ProjectsRoot == "" {
		t.Errorf("ProjectsRoot empty (regression)")
	}
}

func TestLoadOverridesDisplay(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[display]
mode = "cost"
cost_warn_usd = 7.5
cost_hot_usd = 15.0
`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.Mode != "cost" || cfg.Display.CostWarnUSD != 7.5 || cfg.Display.CostHotUSD != 15.0 {
		t.Errorf("overrides not applied: %+v", cfg.Display)
	}
}

func TestLoadIgnoresLegacyPlanKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[display]
cost_warn_usd = 0
cost_hot_usd = 0
mode = "auto"

[plan]
tier = "max_5x"
custom_ceiling_tokens = 12345
api_warn_usd = 8.0
api_hot_usd = 16.0
`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.CostWarnUSD != 8.0 {
		t.Errorf("api_warn_usd → cost_warn_usd lost: got %v", cfg.Display.CostWarnUSD)
	}
	if cfg.Display.CostHotUSD != 16.0 {
		t.Errorf("api_hot_usd → cost_hot_usd lost: got %v", cfg.Display.CostHotUSD)
	}
	if cfg.Display.Mode != "auto" {
		t.Errorf("Display.Mode = %q, want auto (legacy plan.tier shouldn't force percent)", cfg.Display.Mode)
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

func TestLegacyTierAPIMigratesToCostMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[plan]
tier = "api"
`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.Mode != "cost" {
		t.Errorf("legacy tier=api should migrate to display.mode=cost, got %q", cfg.Display.Mode)
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
