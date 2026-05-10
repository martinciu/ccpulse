// Package state stores per-machine TUI runtime preferences (last-used
// scope, last-used tab) outside the user-edited config.toml.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

type State struct {
	LiveScope string `json:"live_scope"`
	Tab       string `json:"tab"`
}

func Path() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "ccpulse", "state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "ccpulse", "state.json")
}

func Load() State {
	var s State
	data, err := os.ReadFile(Path())
	if err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func Save(s State) error {
	if err := secfile.MkdirAll(filepath.Dir(Path())); err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	return secfile.WriteFile(Path(), data)
}
