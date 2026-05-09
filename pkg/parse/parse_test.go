package parse

import (
	"os"
	"testing"
	"time"
)

func TestParseSingleAssistant(t *testing.T) {
	f, err := os.Open("testdata/single_assistant.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	msgs, err := Parse(f, "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	wantTS, _ := time.Parse(time.RFC3339Nano, "2026-05-09T10:00:00.000Z")
	if !m.Timestamp.Equal(wantTS) {
		t.Errorf("Timestamp = %v, want %v", m.Timestamp, wantTS)
	}
	if m.SessionID != "abc-123" {
		t.Errorf("SessionID = %q", m.SessionID)
	}
	if m.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q", m.Model)
	}
	if m.InputTokens != 100 {
		t.Errorf("InputTokens = %d", m.InputTokens)
	}
	if m.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d", m.OutputTokens)
	}
	if m.CacheReadTokens != 1000 {
		t.Errorf("CacheReadTokens = %d", m.CacheReadTokens)
	}
	if m.CacheWrite5mTokens != 150 {
		t.Errorf("CacheWrite5mTokens = %d", m.CacheWrite5mTokens)
	}
	if m.CacheWrite1hTokens != 50 {
		t.Errorf("CacheWrite1hTokens = %d", m.CacheWrite1hTokens)
	}
	if m.ProjectSlug != "test-slug" {
		t.Errorf("ProjectSlug = %q", m.ProjectSlug)
	}
}

func TestParseMixedLines(t *testing.T) {
	f, err := os.Open("testdata/mixed_lines.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	msgs, err := Parse(f, "slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Model != "claude-sonnet-4-6" {
		t.Errorf("msgs[0].Model = %q", msgs[0].Model)
	}
	if msgs[1].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("msgs[1].Model = %q", msgs[1].Model)
	}
}

func TestParseReportsMalformed(t *testing.T) {
	f, err := os.Open("testdata/mixed_lines.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	msgs, errs, err := ParseWithErrors(f, "slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages: got %d, want 2", len(msgs))
	}
	if len(errs) != 1 {
		t.Fatalf("errs: got %d, want 1", len(errs))
	}
	if errs[0].Line != 4 {
		t.Errorf("err line = %d, want 4", errs[0].Line)
	}
}
