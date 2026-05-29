package parse

import (
	"bufio"
	"errors"
	"os"
	"strings"
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
	if m.Cwd != "/Users/x/proj" {
		t.Errorf("Cwd = %q", m.Cwd)
	}
	if m.GitBranch != "main" {
		t.Errorf("GitBranch = %q", m.GitBranch)
	}
}

func TestParseCapturesCwdAndGitBranch(t *testing.T) {
	f, err := os.Open("testdata/with_envelope_fields.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	msgs, err := Parse(f, "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}

	with := msgs[0]
	if with.Cwd != "/Users/x/proj" {
		t.Errorf("with.Cwd = %q, want /Users/x/proj", with.Cwd)
	}
	if with.GitBranch != "feature/x" {
		t.Errorf("with.GitBranch = %q, want feature/x", with.GitBranch)
	}

	without := msgs[1]
	if without.Cwd != "" {
		t.Errorf("without.Cwd = %q, want empty", without.Cwd)
	}
	if without.GitBranch != "" {
		t.Errorf("without.GitBranch = %q, want empty", without.GitBranch)
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

func TestParseWithErrors_ReportsOversizedLine(t *testing.T) {
	withScannerMaxBytes(t)

	valid := validAssistantLine("")
	big := `{"type":"assistant","padding":"` + strings.Repeat("x", 5000) + `"}` + "\n"

	var buf strings.Builder
	buf.WriteString(valid)
	buf.WriteString(valid)
	buf.WriteString(big)
	buf.WriteString(valid) // not yielded — io.Reader path stops at overflow
	buf.WriteString(valid)

	msgs, errs, err := ParseWithErrors(strings.NewReader(buf.String()), "slug")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(msgs) != 2 {
		t.Errorf("len(msgs) = %d, want 2", len(msgs))
	}
	if len(errs) != 1 {
		t.Fatalf("len(errs) = %d, want 1", len(errs))
	}
	if !errors.Is(errs[0].Err, ErrOversizedLineSkipped) {
		t.Errorf("errs[0].Err not ErrOversizedLineSkipped: %v", errs[0].Err)
	}
	if !errors.Is(errs[0].Err, bufio.ErrTooLong) {
		t.Errorf("errs[0].Err not wrapping bufio.ErrTooLong: %v", errs[0].Err)
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

func TestParse_CapturesMessageID(t *testing.T) {
	f, err := os.Open("testdata/with_message_id.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	msgs, err := Parse(f, "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	for i := range 3 {
		if msgs[i].MessageID != "msg_01EaHHYYfAp2yyszT7wAq64w" {
			t.Errorf("msgs[%d].MessageID = %q, want msg_01EaHHYYfAp2yyszT7wAq64w", i, msgs[i].MessageID)
		}
	}
	if msgs[3].MessageID != "" {
		t.Errorf("msgs[3].MessageID = %q, want empty (line has no message.id)", msgs[3].MessageID)
	}
}
