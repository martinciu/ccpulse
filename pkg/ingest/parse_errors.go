// Package ingest catches the ccpulse cache up to current EOF
// for one or many .jsonl transcripts. Used by both the watcher
// callback (one file per fsnotify event) and the startup
// backfill loop (every .jsonl under projectsRoot).
package ingest

import (
	"fmt"
	"strconv"

	"github.com/martinciu/ccpulse/pkg/logfile"
	"github.com/martinciu/ccpulse/pkg/parse"
)

// AppendParseErrors writes per-line parse errors to a log file,
// rotating once the file exceeds 10 MB by truncating it and starting
// fresh. Source path and error message are wrapped with
// strconv.QuoteToASCII so embedded ANSI escapes, control chars, and
// newlines cannot reach the terminal raw. Best-effort — any error is
// swallowed.
func AppendParseErrors(logPath, source string, perrs []parse.ParseError) {
	f := logfile.OpenRotated(logPath)
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
	f := logfile.OpenRotated(logPath)
	if f == nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s: %s\n", strconv.QuoteToASCII(source), strconv.QuoteToASCII(err.Error()))
}
