package config

import (
	_ "embed"
	"os"

	"github.com/BurntSushi/toml"
)

//go:embed default.toml
var defaultTOML []byte

type UI struct {
	Accent       string `toml:"accent"`
	DefaultTab   string `toml:"default_tab"`
	DefaultScope string `toml:"default_scope"`
	TickMs       int    `toml:"tick_ms"`
}

type History struct {
	DefaultWindowDays int  `toml:"default_window_days"`
	IncludeSubagents  bool `toml:"include_subagents"`
	RetentionDays     int  `toml:"retention_days"`
}

type Paths struct {
	ProjectsRoot string `toml:"projects_root"`
	CacheDir     string `toml:"cache_dir"`
}

type Pricing struct {
	Override string `toml:"override"`
}

type Config struct {
	UI      UI      `toml:"ui"`
	History History `toml:"history"`
	Paths   Paths   `toml:"paths"`
	Pricing Pricing `toml:"pricing"`
}

// Load reads cfg from path, falling back to embedded defaults if path is empty.
// Defaults always apply for unset fields. Unknown top-level sections (including
// the dropped [display] and the legacy [plan]) are silently ignored.
func Load(path string) (Config, error) {
	var cfg Config
	if _, err := toml.Decode(string(defaultTOML), &cfg); err != nil {
		return cfg, err
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// DefaultPath returns the OS-appropriate config path, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x + "/ccpulse/config.toml"
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/ccpulse/config.toml"
}
