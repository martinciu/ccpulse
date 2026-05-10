package parse

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// ScannerMaxBytes is the upper bound passed to bufio.Scanner.Buffer at
// every scanning site in this package. Set to 64 MiB in production —
// large enough to absorb any plausible single JSONL line (giant tool
// results, base64-embedded screenshots, full-file Read outputs). The
// recovery path for ErrTooLong is the backstop above this ceiling.
//
// Exposed as a var (not a const) so tests can shrink it cheaply to
// trigger ErrTooLong without synthesising 64 MiB lines.
var ScannerMaxBytes = 64 << 20

// ParseFromOffsetWithErrors is ParseFromOffset that also returns
// per-line ParseError records for malformed JSON. Each error's Line
// field is the absolute line number in the source file.
func ParseFromOffsetWithErrors(path, slug string, startOffset int64, startLine int) ([]Message, []ParseError, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer f.Close()

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, nil, 0, 0, err
	}

	var msgs []Message
	var errs []ParseError
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), ScannerMaxBytes)
	off := startOffset
	line := startLine
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		off += int64(len(raw)) + 1
		var r rawLine
		if err := json.Unmarshal(raw, &r); err != nil {
			errs = append(errs, ParseError{Line: line, Err: err})
			continue
		}
		if r.Type == "assistant" {
			msgs = append(msgs, toMessage(r, slug))
		}
	}
	return msgs, errs, off, line, sc.Err()
}
