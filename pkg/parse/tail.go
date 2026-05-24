package parse

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// ParseFromOffset opens path, seeks to startOffset, and parses to EOF.
// Returns new messages, the post-parse byte offset, and the post-parse
// line number. Lines starting before startOffset are skipped entirely.
//
// On bufio.ErrTooLong the cursor is advanced past the oversized line
// (mirroring ParseFromOffsetWithErrors). The synthesised parse-error
// has nowhere to go in this variant, so the skip is silent — same
// shape as the silent JSON-unmarshal-error swallow already in this
// loop. Callers that need parse-error visibility use the WithErrors
// variant.
//
//nolint:revive // ParseFromOffset stutters; retained for compatibility — part of the established incremental-tail API
func ParseFromOffset(path, slug string, startOffset int64, startLine int) ([]Message, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}

	var msgs []Message
	off := startOffset
	line := startLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)

	for {
		for sc.Scan() {
			line++
			raw := sc.Bytes()
			off += int64(len(raw)) + 1
			var r rawLine
			if err := json.Unmarshal(raw, &r); err != nil {
				continue
			}
			if r.Type == "assistant" {
				msgs = append(msgs, toMessage(r, slug))
			}
		}
		serr := sc.Err()
		if serr == nil {
			return msgs, off, line, nil
		}
		if !errors.Is(serr, bufio.ErrTooLong) {
			return msgs, off, line, serr
		}
		skipped, found, sErr := skipPastNewline(f, off)
		if sErr != nil {
			return msgs, off, line, sErr
		}
		if !found {
			return msgs, off, line, nil
		}
		line++
		off += int64(skipped) + 1
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return msgs, off, line, err
		}
		sc = bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)
	}
}
