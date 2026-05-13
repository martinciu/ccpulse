// Package devlog wires slog.Default() based on the build channel.
//
// In dev: writes DEBUG-level output to <cacheDir>/debug.log (append).
// In release: discards all slog output.
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

// Init configures slog.Default() based on isDev. Returns the opened log
// file (caller may close on shutdown) in dev, or nil in release.
//
// Init is best-effort: on any failure path slog.Default is set to the
// discard handler so the binary keeps running quietly even if logging
// could not be set up.
func Init(isDev bool, cacheDir string) (io.Closer, error) {
	discard := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	if !isDev {
		slog.SetDefault(discard)
		return nil, nil
	}
	if err := secfile.MkdirAll(cacheDir); err != nil {
		slog.SetDefault(discard)
		return nil, err
	}
	f, err := secfile.OpenFile(
		filepath.Join(cacheDir, "debug.log"),
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
		Level:           log.DebugLevel,
	})
	slog.SetDefault(slog.New(handler))
	return f, nil
}
