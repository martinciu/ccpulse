// Package config loads the TOML config at ~/.config/ccpulse/config.toml.
package config

import (
	_ "embed"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/martinciu/ccpulse/pkg/channel"
)

//go:embed default.toml
var defaultTOML []byte

// History holds transcript retention settings.
type History struct {
	RetentionDays int `toml:"retention_days"`
}

// Paths holds filesystem root and cache-directory overrides.
type Paths struct {
	ProjectsRoot string `toml:"projects_root"`
	CacheDir     string `toml:"cache_dir"`
}

// UI holds TUI presentation preferences such as reduced-motion mode.
type UI struct {
	ReduceMotion bool `toml:"reduce_motion"`
}

// Config is the top-level configuration structure decoded from config.toml.
type Config struct {
	History History `toml:"history"`
	Paths   Paths   `toml:"paths"`
	UI      UI      `toml:"ui"`
}

// Load reads cfg from path, falling back to embedded defaults if path is empty.
// Defaults always apply for unset fields. Unknown top-level sections and keys —
// including the dropped [display] / [pricing] sections, the legacy [plan] section,
// and the dropped history.default_window_days / history.include_subagents keys —
// are silently ignored, so older user configs keep loading without errors.
//
// An empty cache_dir (the default) resolves to the channel-appropriate
// path: "~/.cache/ccpulse" on the release channel, "~/.cache/ccpulse-dev"
// on the dev channel. User-explicit values override this resolution.
//
// The channel-aware fallback applies even when the user's config file
// is missing — callers that ignore the returned error (doctor, runTUI,
// runIndex) still get a usable cache_dir.
func Load(path string) (Config, error) {
	cfg, err := decode(path)
	if cfg.Paths.CacheDir == "" {
		cfg.Paths.CacheDir = defaultCacheDir()
	}
	return cfg, err
}

// decode handles only the embedded-default + user-file decoding.
// Channel-aware defaults are applied by Load.
func decode(path string) (Config, error) {
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

// defaultCacheDir is the channel-aware fallback for an empty cache_dir.
// Tilde expansion happens at the call site (cmd/ccpulse uses `expand`).
func defaultCacheDir() string {
	if channel.IsDev() {
		return "~/.cache/ccpulse-dev"
	}
	return "~/.cache/ccpulse"
}

// DefaultPath returns the OS-appropriate config path, honoring XDG_CONFIG_HOME.
// On the dev channel the project segment becomes "ccpulse-dev" so dev runs
// never read or overwrite the released config file.
func DefaultPath() string {
	project := "ccpulse"
	if channel.IsDev() {
		project = "ccpulse-dev"
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x + "/" + project + "/config.toml"
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/" + project + "/config.toml"
}
