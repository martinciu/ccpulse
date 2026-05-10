package parse

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// ParseFromOffset opens path, seeks to startOffset, and parses to EOF.
// Returns new messages, the post-parse byte offset, and the post-parse
// line number. Lines starting before startOffset are skipped entirely.
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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)
	off := startOffset
	line := startLine
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		off += int64(len(raw)) + 1 // +1 for \n
		var r rawLine
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		if r.Type == "assistant" {
			msgs = append(msgs, toMessage(r, slug))
		}
	}
	return msgs, off, line, sc.Err()
}
