package config

import (
	_ "embed"
	"os"

	"github.com/BurntSushi/toml"
)

//go:embed default.toml
var defaultTOML []byte

// Display controls the tmux/TUI presentation. Mode is one of "auto", "percent",
// "cost". "auto" picks based on whether an OAuth credential is found.
type Display struct {
	Mode        string  `toml:"mode"`
	CostWarnUSD float64 `toml:"cost_warn_usd"`
	CostHotUSD  float64 `toml:"cost_hot_usd"`
}

type UI struct {
	Accent       string `toml:"accent"`
	DefaultTab   string `toml:"default_tab"`
	DefaultScope string `toml:"default_scope"`
	TickMs       int    `toml:"tick_ms"`
}

type History struct {
	DefaultWindowDays int  `toml:"default_window_days"`
	IncludeSubagents  bool `toml:"include_subagents"`
}

type Paths struct {
	ProjectsRoot string `toml:"projects_root"`
	CacheDir     string `toml:"cache_dir"`
}

type Pricing struct {
	Override string `toml:"override"`
}

// legacyPlan is the old [plan] section. We still parse it so existing
// config files load without errors, then map relevant fields into Display.
type legacyPlan struct {
	Tier                string  `toml:"tier"`
	CustomCeilingTokens int64   `toml:"custom_ceiling_tokens"`
	APIWarnUSD          float64 `toml:"api_warn_usd"`
	APIHotUSD           float64 `toml:"api_hot_usd"`
}

type Config struct {
	Display Display `toml:"display"`
	UI      UI      `toml:"ui"`
	History History `toml:"history"`
	Paths   Paths   `toml:"paths"`
	Pricing Pricing `toml:"pricing"`

	// LegacyPlan is populated when loading an old-format config; surfaced
	// so callers can warn the user. Not exported via toml encode since we
	// drop it from output.
	LegacyPlan legacyPlan `toml:"plan"`
}

// Load reads cfg from path, falling back to embedded defaults if path
// is empty. Defaults always apply for unset fields. Legacy [plan] keys
// migrate into [display] when the new keys are at their zero values.
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
	migrateLegacy(&cfg)
	return cfg, nil
}

// migrateLegacy maps old [plan] fields into [display]. Idempotent — calling
// twice has the same effect as once. We only fill new keys that are still at
// their default (zero / "auto") so explicit user values always win.
func migrateLegacy(cfg *Config) {
	lp := cfg.LegacyPlan
	if cfg.Display.CostWarnUSD == 0 && lp.APIWarnUSD > 0 {
		cfg.Display.CostWarnUSD = lp.APIWarnUSD
	}
	if cfg.Display.CostHotUSD == 0 && lp.APIHotUSD > 0 {
		cfg.Display.CostHotUSD = lp.APIHotUSD
	}
	if cfg.Display.Mode == "auto" && lp.Tier == "api" {
		cfg.Display.Mode = "cost"
	}
}

// DefaultPath returns the OS-appropriate config path, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x + "/ccpulse/config.toml"
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/ccpulse/config.toml"
}

// HasLegacyPlan reports whether the loaded config used the deprecated
// [plan] section. Callers can warn once and continue.
func (c Config) HasLegacyPlan() bool {
	lp := c.LegacyPlan
	return lp.Tier != "" || lp.CustomCeilingTokens != 0 || lp.APIWarnUSD != 0 || lp.APIHotUSD != 0
}
