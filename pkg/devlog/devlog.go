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
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

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
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return f, nil
}
