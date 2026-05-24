package parse

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withScannerMaxBytes shrinks ScannerMaxBytes for the duration of
// the test, so we can trigger ErrTooLong with a small synthesised
// line (rather than 64 MiB).
func withScannerMaxBytes(t *testing.T, n int) {
	t.Helper()
	prev := ScannerMaxBytes
	ScannerMaxBytes = n
	t.Cleanup(func() { ScannerMaxBytes = prev })
}

// validAssistantLine returns a single complete JSONL line for an
// assistant message, with the given filler appended inside a
// "padding" field so callers can inflate it to any size.
func validAssistantLine(filler string) string {
	return `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}},"sessionId":"s","timestamp":"2026-05-09T10:00:00.000Z","padding":"` + filler + `"}` + "\n"
}

func TestSkipPastNewline_FindsTerminator(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.bin")
	// "before\nafter" — newline at index 6, so skipping from offset 0
	// should report 6 bytes scanned and found=true.
	if err := os.WriteFile(p, []byte("before\nafter"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	skipped, found, err := skipPastNewline(f, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !found {
		t.Errorf("found = false, want true")
	}
	if skipped != 6 {
		t.Errorf("skipped = %d, want 6", skipped)
	}
}

func TestSkipPastNewline_NoTerminatorBeforeEOF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.bin")
	if err := os.WriteFile(p, []byte("nonewline"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	skipped, found, err := skipPastNewline(f, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if found {
		t.Errorf("found = true, want false")
	}
	if skipped != 9 {
		t.Errorf("skipped = %d, want 9", skipped)
	}
}

func TestSkipPastNewline_StartsFromOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.bin")
	// "skip-me\nstart-here\nrest" — start scanning at offset 8
	// (just past the first \n). Next \n is at index 18, so skipped == 10.
	if err := os.WriteFile(p, []byte("skip-me\nstart-here\nrest"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	skipped, found, err := skipPastNewline(f, 8)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !found || skipped != 10 {
		t.Errorf("(skipped, found) = (%d, %v), want (10, true)", skipped, found)
	}
}

func TestParseFromOffsetWithErrors_SkipsOversizedLine(t *testing.T) {
	withScannerMaxBytes(t, 4096) // 4 KiB ceiling for cheap overflow

	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")

	// 25 small valid lines, 1 oversized (>4 KiB), 25 more valid.
	var b []byte
	for range 25 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	preOff := int64(len(b))
	bigFiller := strings.Repeat("x", 5000) // line will be > 4 KiB
	bigLine := validAssistantLine(bigFiller)
	bigSize := int64(len(bigLine)) - 1 // size excluding trailing '\n'
	b = append(b, []byte(bigLine)...)
	for range 25 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, errs, newOff, newLine, err := ParseFromOffsetWithErrors(p, "slug", 0, 0)
	if err != nil {
		t.Fatalf("parseErr = %v, want nil", err)
	}
	if len(msgs) != 50 {
		t.Errorf("len(msgs) = %d, want 50", len(msgs))
	}
	if len(errs) != 1 {
		t.Fatalf("len(errs) = %d, want 1", len(errs))
	}
	if errs[0].Line != 26 {
		t.Errorf("errs[0].Line = %d, want 26", errs[0].Line)
	}
	if !errors.Is(errs[0].Err, ErrOversizedLineSkipped) {
		t.Errorf("errs[0].Err not ErrOversizedLineSkipped: %v", errs[0].Err)
	}
	if !errors.Is(errs[0].Err, bufio.ErrTooLong) {
		t.Errorf("errs[0].Err not wrapping bufio.ErrTooLong: %v", errs[0].Err)
	}
	wantOffset := fmt.Sprintf("offset %d", preOff)
	if !strings.Contains(errs[0].Err.Error(), wantOffset) {
		t.Errorf("errs[0].Err = %q, want containing %q", errs[0].Err.Error(), wantOffset)
	}
	if int64(len(b)) != newOff {
		t.Errorf("newOff = %d, want %d (file size)", newOff, len(b))
	}
	if newLine != 51 {
		t.Errorf("newLine = %d, want 51", newLine)
	}
	if bigSize <= int64(ScannerMaxBytes) {
		t.Fatalf("test setup wrong: bigSize=%d not > ScannerMaxBytes=%d", bigSize, ScannerMaxBytes)
	}
}

func TestParseFromOffsetWithErrors_IdempotentReparse(t *testing.T) {
	withScannerMaxBytes(t, 4096)

	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")

	var b []byte
	for range 5 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	b = append(b, []byte(validAssistantLine(strings.Repeat("x", 5000)))...)
	for range 5 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, newOff, newLine, err := ParseFromOffsetWithErrors(p, "slug", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	msgs2, errs2, newOff2, newLine2, err := ParseFromOffsetWithErrors(p, "slug", newOff, newLine)
	if err != nil {
		t.Fatalf("second pass err = %v", err)
	}
	if len(msgs2) != 0 {
		t.Errorf("second pass len(msgs) = %d, want 0", len(msgs2))
	}
	if len(errs2) != 0 {
		t.Errorf("second pass len(errs) = %d, want 0", len(errs2))
	}
	if newOff2 != newOff {
		t.Errorf("second pass newOff = %d, want unchanged %d", newOff2, newOff)
	}
	if newLine2 != newLine {
		t.Errorf("second pass newLine = %d, want unchanged %d", newLine2, newLine)
	}
}

func TestParseFromOffsetWithErrors_OversizedTailNoNewline(t *testing.T) {
	withScannerMaxBytes(t, 4096)

	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")

	var b []byte
	for range 5 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	preTailOff := int64(len(b))
	// Oversized bytes still being written (no terminating '\n').
	b = append(b, []byte(`{"type":"assistant","padding":"`)...)
	b = append(b, []byte(strings.Repeat("x", 5000))...)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, errs, newOff, newLine, err := ParseFromOffsetWithErrors(p, "slug", 0, 0)
	if err != nil {
		t.Fatalf("parseErr = %v, want nil", err)
	}
	if len(msgs) != 5 {
		t.Errorf("len(msgs) = %d, want 5", len(msgs))
	}
	if len(errs) != 0 {
		t.Errorf("len(errs) = %d, want 0 (no synthesised entry yet — line in progress)", len(errs))
	}
	if newOff != preTailOff {
		t.Errorf("newOff = %d, want %d (right before oversized tail)", newOff, preTailOff)
	}
	if newLine != 5 {
		t.Errorf("newLine = %d, want 5", newLine)
	}
}

func TestParseFromOffsetWithErrors_FirstLineOversized(t *testing.T) {
	withScannerMaxBytes(t, 4096)

	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")

	var b []byte
	b = append(b, []byte(validAssistantLine(strings.Repeat("x", 5000)))...)
	for range 3 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, errs, newOff, newLine, err := ParseFromOffsetWithErrors(p, "slug", 0, 0)
	if err != nil {
		t.Fatalf("parseErr = %v, want nil", err)
	}
	if len(msgs) != 3 {
		t.Errorf("len(msgs) = %d, want 3", len(msgs))
	}
	if len(errs) != 1 {
		t.Fatalf("len(errs) = %d, want 1", len(errs))
	}
	if errs[0].Line != 1 {
		t.Errorf("errs[0].Line = %d, want 1 (oversized line is line 1)", errs[0].Line)
	}
	if !errors.Is(errs[0].Err, ErrOversizedLineSkipped) {
		t.Errorf("errs[0].Err not ErrOversizedLineSkipped: %v", errs[0].Err)
	}
	if !strings.Contains(errs[0].Err.Error(), "offset 0") {
		t.Errorf("errs[0].Err = %q, want containing 'offset 0'", errs[0].Err.Error())
	}
	if newOff != int64(len(b)) {
		t.Errorf("newOff = %d, want %d (file size)", newOff, len(b))
	}
	if newLine != 4 {
		t.Errorf("newLine = %d, want 4", newLine)
	}
}

func TestParseFromOffsetWithErrors_BackToBackOversizedLines(t *testing.T) {
	withScannerMaxBytes(t, 4096)

	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")

	// 2 small valid, then 3 oversized lines back-to-back, then 2 small valid.
	// Each ErrTooLong should drive the recovery loop through one more iteration;
	// the second iteration is the case the rest of the suite never exercises.
	var b []byte
	for range 2 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	for range 3 {
		b = append(b, []byte(validAssistantLine(strings.Repeat("x", 5000)))...)
	}
	for range 2 {
		b = append(b, []byte(validAssistantLine(""))...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, errs, newOff, newLine, err := ParseFromOffsetWithErrors(p, "slug", 0, 0)
	if err != nil {
		t.Fatalf("parseErr = %v, want nil", err)
	}
	if len(msgs) != 4 {
		t.Errorf("len(msgs) = %d, want 4", len(msgs))
	}
	if len(errs) != 3 {
		t.Fatalf("len(errs) = %d, want 3 (one synthesised entry per oversized line)", len(errs))
	}
	for i, pe := range errs {
		if !errors.Is(pe.Err, ErrOversizedLineSkipped) {
			t.Errorf("errs[%d].Err not ErrOversizedLineSkipped: %v", i, pe.Err)
		}
	}
	wantLines := []int{3, 4, 5}
	for i, want := range wantLines {
		if errs[i].Line != want {
			t.Errorf("errs[%d].Line = %d, want %d", i, errs[i].Line, want)
		}
	}
	if newOff != int64(len(b)) {
		t.Errorf("newOff = %d, want %d (file size)", newOff, len(b))
	}
	if newLine != 7 {
		t.Errorf("newLine = %d, want 7", newLine)
	}
}

func TestParseFromOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	one := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}},"sessionId":"s","timestamp":"2026-05-09T10:00:00.000Z"}` + "\n"
	if err := os.WriteFile(p, []byte(one+one), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, newOff, newLine, err := ParseFromOffset(p, "slug", int64(len(one)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d, want 1 (skip first via offset)", len(msgs))
	}
	if newOff != int64(2*len(one)) {
		t.Errorf("newOff = %d, want %d", newOff, 2*len(one))
	}
	if newLine != 2 {
		t.Errorf("newLine = %d, want 2", newLine)
	}
}
