// Package ingest catches the ccpulse cache up to current EOF
// for one or many .jsonl transcripts. Used by both the watcher
// callback (one file per fsnotify event) and the startup
// backfill loop (every .jsonl under projectsRoot).
package ingest

import (
	"fmt"
	"os"

	"github.com/martinciu/ccpulse/pkg/parse"
)

const parseErrorsMaxBytes = 10 * 1024 * 1024 // 10 MB

// openRotated opens logPath for append, after first removing the
// file when its current size exceeds parseErrorsMaxBytes. Returns
// nil on any error so callers can no-op silently (best-effort logging).
func openRotated(logPath string) *os.File {
	if info, err := os.Stat(logPath); err == nil && info.Size() > parseErrorsMaxBytes {
		_ = os.Remove(logPath)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	return f
}

// AppendParseErrors writes per-line parse errors to a log file,
// rotating once the file exceeds 10 MB by truncating it and starting
// fresh. Best-effort — any error is swallowed.
func AppendParseErrors(logPath, source string, perrs []parse.ParseError) {
	f := openRotated(logPath)
	if f == nil {
		return
	}
	defer f.Close()
	for _, pe := range perrs {
		fmt.Fprintf(f, "%s:%d %v\n", source, pe.Line, pe.Err)
	}
}

// LogFileError appends a single file-level error (open / stat / etc.)
// to the same rotated log, in a slightly different shape from the
// per-line records: "<source>: <err>". Best-effort.
func LogFileError(logPath, source string, err error) {
	f := openRotated(logPath)
	if f == nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s: %v\n", source, err)
}
