package parse

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

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
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
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
