package parse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkipPastNewline_FindsTerminator(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.bin")
	// "before\nafter" — newline at index 6, so skipping from offset 0
	// should report 6 bytes scanned and found=true.
	if err := os.WriteFile(p, []byte("before\nafter"), 0644); err != nil {
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
	if err := os.WriteFile(p, []byte("nonewline"), 0644); err != nil {
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
	if err := os.WriteFile(p, []byte("skip-me\nstart-here\nrest"), 0644); err != nil {
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

func TestParseFromOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	one := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}},"sessionId":"s","timestamp":"2026-05-09T10:00:00.000Z"}` + "\n"
	if err := os.WriteFile(p, []byte(one+one), 0644); err != nil {
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
