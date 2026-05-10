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

// skipPastNewline reads from f starting at startOff in 64 KiB chunks
// until a '\n' byte is seen. Returns the number of bytes scanned
// before the terminator (or total bytes scanned if EOF was reached
// without finding one), whether a terminator was found, and any read
// error other than io.EOF.
func skipPastNewline(f *os.File, startOff int64) (int, bool, error) {
	if _, err := f.Seek(startOff, io.SeekStart); err != nil {
		return 0, false, err
	}
	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	scanned := 0
	for {
		n, err := f.Read(buf)
		for i := range n {
			if buf[i] == '\n' {
				return scanned + i, true, nil
			}
		}
		scanned += n
		if err == io.EOF {
			return scanned, false, nil
		}
		if err != nil {
			return scanned, false, err
		}
	}
}
