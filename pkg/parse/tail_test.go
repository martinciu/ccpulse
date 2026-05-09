package parse

import (
	"os"
	"path/filepath"
	"testing"
)

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
