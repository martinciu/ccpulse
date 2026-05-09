package config

import (
	"os"
	"path/filepath"
	"testing"
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
