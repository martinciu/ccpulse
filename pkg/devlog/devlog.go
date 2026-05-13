// Package devlog wires slog.Default() based on the build channel and the
// resolved log level.
//
// In dev: writes to <CacheDir>/debug.log at DEBUG or higher (depending on
// Options.Level).
//
// In release: writes to <CacheDir>/ccpulse.log at INFO or higher (depending
// on Options.Level). Set Options.Level to LevelOff to disable logging
// entirely; no file is opened and slog.Default() is set to a discard handler.
//
// External rotation: this package does not rotate logs. Users who want
// bounded log size should rotate the log file externally (e.g. logrotate).
// Because the file is held open for append, external mv/rm will not take
// effect for the running ccpulse — restart to pick up the new inode.
//
// Init is best-effort: on any failure (mkdir or open) it sets slog.Default
// to the discard handler before returning the error, so callers can ignore
// the error and the binary keeps running quietly.
package devlog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/martinciu/ccpulse/pkg/secfile"
)

// LevelOff is a sentinel slog.Level above every real level. Init treats it
// specially: no file is opened, slog.Default() is set to a discard handler.
// Mapped to LevelError + 4 so future custom levels between Error and Off
// remain expressible without renumbering.
const LevelOff slog.Level = slog.LevelError + 4

// ParseLevel converts a flag value to a slog.Level. The accepted set is
// off | error | warn | info | debug (case-insensitive). Unknown values
// return an error so cobra reports the flag failure cleanly.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "off":
		return LevelOff, nil
	case "error":
		return slog.LevelError, nil
	case "warn":
		return slog.LevelWarn, nil
	case "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want off|error|warn|info|debug)", s)
	}
}

// Options configures Init. Construct one per process. CacheDir must be the
// resolved (tilde-expanded, env-overridden) cache directory; Init does not
// re-expand. Level is the resolved level (see ParseLevel).
type Options struct {
	IsDev    bool
	CacheDir string
	Level    slog.Level
}

// Init configures slog.Default() per opts. Returns the opened log file
// (caller may close on shutdown) when a file is opened, or nil when no
// file is needed (Level == LevelOff).
//
// Init is best-effort: on any failure path slog.Default is set to the
// discard handler so the binary keeps running quietly even if logging
// could not be set up.
func Init(opts Options) (io.Closer, error) {
	// Discard handler is constructed at LevelOff so Enabled() reports false
	// at every real level — callers that branch on Enabled() see a truly
	// silent default rather than the stdlib's LevelInfo default.
	discard := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: LevelOff}))
	if opts.Level == LevelOff {
		slog.SetDefault(discard)
		return nil, nil
	}
	if err := secfile.MkdirAll(opts.CacheDir); err != nil {
		slog.SetDefault(discard)
		return nil, err
	}
	name := "ccpulse.log"
	if opts.IsDev {
		name = "debug.log"
	}
	f, err := secfile.OpenFile(
		filepath.Join(opts.CacheDir, name),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
	)
	if err != nil {
		slog.SetDefault(discard)
		return nil, err
	}
	handler := log.NewWithOptions(f, log.Options{
		ReportTimestamp: true,
		ReportCaller:    true,
		TimeFormat:      "2006-01-02 15:04:05.000",
		Level:           charmLevel(opts.Level),
	})
	slog.SetDefault(slog.New(handler))
	return f, nil
}

// charmLevel maps a slog.Level to the corresponding charmbracelet/log
// level. charm/log's level constants (DebugLevel=-4 .. ErrorLevel=8)
// match slog's numeric values for the standard levels, so the cast is safe.
func charmLevel(l slog.Level) log.Level {
	return log.Level(l)
}
