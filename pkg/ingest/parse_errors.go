// Package ingest catches the ccpulse cache up to current EOF
// for one or many .jsonl transcripts. Used by both the watcher
// callback (one file per fsnotify event) and the startup
// backfill loop (every .jsonl under projectsRoot).
package ingest

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/secfile"
)

const parseErrorsMaxBytes = 10 * 1024 * 1024 // 10 MB

// openLogFile opens logPath for append at FileMode. Var so tests can
// shadow it to count calls.
var openLogFile = func(path string) (*os.File, error) {
	return secfile.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
}

// openRotated opens logPath for append, after first removing the
// file when its current size exceeds parseErrorsMaxBytes. Returns
// nil on any error so callers can no-op silently (best-effort logging).
func openRotated(logPath string) *os.File {
	if info, err := os.Stat(logPath); err == nil && info.Size() > parseErrorsMaxBytes {
		_ = os.Remove(logPath)
	}
	f, err := openLogFile(logPath)
	if err != nil {
		return nil
	}
	// Explicitly chmod to 0o600 to tighten pre-existing files where O_CREATE
	// mode didn't apply (issue #224). secfile.OpenFile already does this via
	// os.Chmod, but we add an explicit f.Chmod here as belt-and-suspenders.
	// Best-effort: log on error, don't fail.
	if err := f.Chmod(0o600); err != nil {
		slog.Warn("ingest.openRotated: chmod parse-errors log", "path", logPath, "err", err)
	}
	return f
}

// AppendParseErrors writes per-line parse errors to a log file,
// rotating once the file exceeds 10 MB by truncating it and starting
// fresh. Source path and error message are wrapped with
// strconv.QuoteToASCII so embedded ANSI escapes, control chars, and
// newlines cannot reach the terminal raw. Best-effort — any error is
// swallowed.
func AppendParseErrors(logPath, source string, perrs []parse.ParseError) {
	f := openRotated(logPath)
	if f == nil {
		return
	}
	defer f.Close()
	qsrc := strconv.QuoteToASCII(source)
	for _, pe := range perrs {
		fmt.Fprintf(f, "%s:%d %s\n", qsrc, pe.Line, strconv.QuoteToASCII(pe.Err.Error()))
	}
}

// LogFileError appends a single file-level error (open / stat / etc.)
// to the same rotated log, in a slightly different shape from the
// per-line records: "<source>: <err>". Source and error are sanitized
// per AppendParseErrors. Best-effort.
func LogFileError(logPath, source string, err error) {
	f := openRotated(logPath)
	if f == nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s: %s\n", strconv.QuoteToASCII(source), strconv.QuoteToASCII(err.Error()))
}
