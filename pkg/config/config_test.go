package config

import (
	"testing"
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
