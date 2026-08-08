// Package uistate persists small TUI UI preferences (zoom level, active
// view) to <cacheDir>/ui-state.toml so they survive restarts (#490).
//
// The file is ccpulse-owned (~40 bytes) and written atomically via
// secfile.WriteFileAtomic on every change. Values are stored as names,
// not indices, so a reorder of pkg/tui's ZoomLevels/chartUnit can never
// mis-restore a persisted preference.
package uistate

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

// FileName is the state file's basename inside the cache dir.
const FileName = "ui-state.toml"

// State is the persisted TUI UI state. The zero value means "no
// preference" — consumers fall back to their defaults for any
// empty/unknown field.
type State struct {
	Zoom string `toml:"zoom"` // ZoomLevel label: "15m" | "1h" | "24h"
	View string `toml:"view"` // chart view name: "cost" | "tokens" | "usage"
}

// Load reads the state file under cacheDir. A missing file (first launch)
// returns the zero State silently; any other failure (corrupt TOML,
// permissions) returns the zero State and logs at debug — a UI-state
// problem must never surface in the TUI. Unknown keys in the file are
// ignored (BurntSushi default), so fields added by newer versions don't
// break older binaries.
func Load(cacheDir string) State {
	var s State
	if _, err := toml.DecodeFile(filepath.Join(cacheDir, FileName), &s); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("uistate.load", "err", err)
		}
		return State{}
	}
	return s
}

// Save writes s to <cacheDir>/ui-state.toml atomically at 0600. The
// cache dir must already exist (bootstrap owns its creation); a missing
// dir surfaces as an error for the caller to log.
func Save(cacheDir string, s State) error {
	data, err := toml.Marshal(s)
	if err != nil {
		return err
	}
	return secfile.WriteFileAtomic(filepath.Join(cacheDir, FileName), data)
}
