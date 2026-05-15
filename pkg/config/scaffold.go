package config

import (
	"bytes"

	"github.com/BurntSushi/toml"
)

const scaffoldHeader = "# ccpulse config — managed by you, never overwritten.\n"

// Scaffold returns the bytes written to ~/.config/ccpulse/config.toml on
// first run. Format: scaffoldHeader followed by the resolved Config encoded
// as TOML — the same shape as `ccpulse config show`. Every key ships active
// so the file reads like a working config the user edits in place.
//
// Trade-off: cache_dir is baked to the channel-resolved path at scaffold
// time (e.g. "~/.cache/ccpulse-dev" on dev builds). Switching channels
// later keeps the original value because the user's file overrides the
// channel-aware fallback in Load. Users wanting channel-tracking must
// delete the cache_dir line.
func Scaffold() []byte {
	cfg, _ := Load("") // never errors when path is empty
	var buf bytes.Buffer
	buf.WriteString(scaffoldHeader)
	buf.WriteByte('\n')
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		panic("config.Scaffold: encode Config: " + err.Error())
	}
	return buf.Bytes()
}
