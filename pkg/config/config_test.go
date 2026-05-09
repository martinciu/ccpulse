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
	if cfg.Plan.Tier != "max_20x" {
		t.Errorf("tier = %q, want max_20x", cfg.Plan.Tier)
	}
	if cfg.UI.TickMs != 1000 {
		t.Errorf("tick_ms = %d", cfg.UI.TickMs)
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(p, []byte(`[plan]
tier="custom"
custom_ceiling_tokens=5000000
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plan.Tier != "custom" {
		t.Errorf("tier = %q", cfg.Plan.Tier)
	}
	if cfg.Plan.CustomCeilingTokens != 5000000 {
		t.Errorf("ceiling = %d", cfg.Plan.CustomCeilingTokens)
	}
}
