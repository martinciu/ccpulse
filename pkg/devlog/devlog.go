// Package devlog wires slog.Default() based on the build channel and an
// optional CLI-level override.
//
// In dev: writes to <cacheDir>/debug.log at DEBUG level.
// In release: writes to <cacheDir>/ccpulse.log at INFO level unless
// overridden by --log-level.
//
// Init is best-effort: on any failure path slog.Default is set to the
// discard handler so the binary keeps running quietly even if logging
// could not be set up.
package devlog

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	charmlog "github.com/charmbracelet/log"
	"github.com/martinciu/ccpulse/pkg/channel"
	"github.com/martinciu/ccpulse/pkg/logfile"
	"github.com/martinciu/ccpulse/pkg/secfile"
)

// maxCharmLevel is one above charmlog's highest defined level (FatalLevel=12).
const maxCharmLevel = charmlog.Level(charmlog.FatalLevel) + 1

// charmDiscard is a charmlog.Logger that discards everything (level above Fatal).
var charmDiscard = charmlog.NewWithOptions(io.Discard, charmlog.Options{Level: maxCharmLevel})

// Init configures charmlog.Default() and slog.Default() based on the build
// channel and an optional CLI-provided level override. Returns the opened
// log file (caller closes on shutdown) in dev and release, or nil when level
// is "off".
//
// Init is best-effort: on any failure path the global logger defaults are set
// to discard handlers so the binary keeps running quietly even if logging
// could not be set up.
func Init(cacheDir, levelOverride string) (io.Closer, error) {
	effLevel := effectiveLevel(levelOverride)
	if effLevel == levelOff {
		charmlog.SetDefault(charmDiscard)
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})))
		return nil, nil
	}
	if err := secfile.MkdirAll(cacheDir); err != nil {
		charmlog.SetDefault(charmDiscard)
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})))
		return nil, err
	}
	var filename string
	if channel.IsDev() {
		filename = "debug.log"
	} else {
		filename = "ccpulse.log"
	}
	f := logfile.OpenRotated(filepath.Join(cacheDir, filename))
	if f == nil {
		charmlog.SetDefault(charmDiscard)
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})))
		return nil, nil
	}
	handler := charmlog.NewWithOptions(f, charmlog.Options{
		ReportTimestamp: true,
		ReportCaller:    true,
		TimeFormat:      "2006-01-02 15:04:05.000",
		Level:           effLevel,
	})
	// Set both: charmlog's default (for any charmlog.Info/Warn/Error calls in
	// the codebase) and slog's default (for slog.Info calls).
	charmlog.SetDefault(handler)
	slog.SetDefault(slog.New(handler))
	return f, nil
}

// levelOff is a sentinel: one above charmlog.FatalLevel so it never matches
// any real level and Init returns early before opening a file.
const levelOff = maxCharmLevel

// slog level constants using charmlog's Level type.
const (
	levelDebug = charmlog.DebugLevel
	levelInfo  = charmlog.InfoLevel
	levelWarn  = charmlog.WarnLevel
	levelError = charmlog.ErrorLevel
)

// parseLevel maps a level string to charmlog.Level. Unknown strings return
// the channel default. Empty string also returns the channel default.
func parseLevel(s string) charmlog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug
	case "info":
		return levelInfo
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	case "off":
		return levelOff
	default:
		return -1 // sentinel: use channel default
	}
}

// effectiveLevel returns the charmlog.Level to use given the CLI override
// string and the current build channel's default.
func effectiveLevel(override string) charmlog.Level {
	if override == "" {
		// Use channel default.
		if channel.IsDev() {
			return levelDebug
		}
		return levelInfo
	}
	lvl := parseLevel(override)
	if lvl == -1 {
		// Unknown string — fall back to channel default.
		if channel.IsDev() {
			return levelDebug
		}
		return levelInfo
	}
	return lvl
}