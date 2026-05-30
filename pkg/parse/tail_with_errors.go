package parse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
// trigger ErrTooLong without synthesising 64 MiB lines. Mutators must
// not run in parallel — there is no synchronisation around access.
var ScannerMaxBytes = 64 << 20

// scannerInitialCap is the initial buffer capacity used at every
// Scanner.Buffer site in this package. Capped by ScannerMaxBytes so
// the test seam genuinely lowers the ceiling: bufio.Scanner uses
// max(max, cap(buf)) as the effective ceiling, so cap(buf) must not
// exceed ScannerMaxBytes for tests to trigger ErrTooLong.
func scannerInitialCap() int {
	if 1<<20 < ScannerMaxBytes {
		return 1 << 20
	}
	return ScannerMaxBytes
}

// ParseFromOffsetWithErrors is ParseFromOffset that also returns
// per-line ParseError records for malformed JSON. Each error's Line
// field is the absolute line number in the source file.
//
//nolint:revive // ParseFromOffsetWithErrors stutters; retained for compatibility — part of the established incremental-tail API
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
	off := startOffset
	line := startLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)
	sc.Split(scanCompleteLines)

	for {
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
		serr := sc.Err()
		if serr == nil {
			return msgs, errs, off, line, nil
		}
		if !errors.Is(serr, bufio.ErrTooLong) {
			return msgs, errs, off, line, serr
		}
		// Oversized line begins at `off`. Find the next '\n' from there.
		oversizedStart := off
		skipped, found, sErr := skipPastNewline(f, off)
		if sErr != nil {
			return msgs, errs, off, line, sErr
		}
		if !found {
			// Oversized line is the in-progress tail of the file. Leave
			// `off` where it is; next fs event retries.
			return msgs, errs, off, line, nil
		}
		line++
		errs = append(errs, ParseError{
			Line: line,
			Err:  fmt.Errorf("%w at offset %d (%d bytes): %w", ErrOversizedLineSkipped, oversizedStart, skipped, bufio.ErrTooLong),
		})
		off += int64(skipped) + 1 // +1 for the trailing '\n'
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return msgs, errs, off, line, err
		}
		sc = bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)
		sc.Split(scanCompleteLines)
	}
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
		if errors.Is(err, io.EOF) {
			return scanned, false, nil
		}
		if err != nil {
			return scanned, false, err
		}
	}
}

// scanCompleteLines is a bufio.SplitFunc identical to bufio.ScanLines
// except it never returns a final, non-newline-terminated line. When the
// buffer holds no '\n' it returns (0, nil, nil) regardless of EOF — that
// dropped clause is the whole fix (issue #377):
//   - mid-stream, Scanner reads more (growing to ScannerMaxBytes, then
//     ErrTooLong for a genuinely oversized line);
//   - at EOF, Scan() stops with Err() == nil and the cursor is left at
//     the start of the in-progress line, so the next pass re-reads it from
//     its true start once the writer appends the '\n'.
//
// The atEOF argument is deliberately ignored — see above.
func scanCompleteLines(data []byte, _ bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, dropCR(data[:i]), nil
	}
	return 0, nil, nil
}

// dropCR drops a terminal '\r' from data. bufio's dropCR is unexported, so
// this preserves ScanLines' \r\n handling for a bit-for-bit behaviour match.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}
