package parse

import (
	"bufio"
	"errors"
	"fmt"
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

func TestParseCapturesEffort(t *testing.T) {
	input := `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","effort":"xhigh","message":{"id":"m1","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n" +
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:01.000Z","message":{"id":"m2","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n"

	msgs, err := Parse(strings.NewReader(input), "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Effort != "xhigh" {
		t.Errorf("msgs[0].Effort = %q, want %q", msgs[0].Effort, "xhigh")
	}
	if msgs[1].Effort != "" {
		t.Errorf("msgs[1].Effort = %q, want empty (field absent pre-CC-2.1.212)", msgs[1].Effort)
	}
}

// TestIterationsInformativePredicate drives full JSONL lines through Parse and
// asserts which iterations arrays are kept verbatim vs dropped as redundant.
// Outer usage in every line: input 2, output 300, cache_read 20000,
// cache_creation 400 (5m 0 / 1h 400); outer model claude-fable-5.
func TestIterationsInformativePredicate(t *testing.T) {
	const redundantEntry = `{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}`

	tests := []struct {
		name       string
		iterations string // raw JSON for the iterations value; "" = omit the key
		want       string // expected Message.IterationsJSON
	}{
		{name: "absent", iterations: "", want: ""},
		{name: "json null", iterations: "null", want: ""},
		{name: "empty array", iterations: "[]", want: ""},
		{name: "single redundant", iterations: "[" + redundantEntry + "]", want: ""},
		{
			name: "single redundant with model equal to outer",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-fable-5"}]`,
			want: "",
		},
		{
			name: "single with differing model kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-opus-4-8"}]`,
			want: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-opus-4-8"}]`,
		},
		{
			name: "single non-message type kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"fallback_message"}]`,
			want: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"fallback_message"}]`,
		},
		{
			name: "single token mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":299,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want: `[{"input_tokens":2,"output_tokens":299,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name: "multi-attempt kept",
			iterations: "[" + redundantEntry + `,{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":21000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]`,
			want: "[" + redundantEntry + `,{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":21000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]`,
		},
		{
			name:       "unparseable shape kept verbatim (fail open)",
			iterations: `{"unexpected":"object"}`,
			want:       `{"unexpected":"object"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iterField := ""
			if tt.iterations != "" {
				iterField = `,"iterations":` + tt.iterations
			}
			line := fmt.Sprintf(`{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400}%s}}}`, iterField) + "\n"

			msgs, err := Parse(strings.NewReader(line), "test-slug")
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d messages, want 1", len(msgs))
			}
			if msgs[0].IterationsJSON != tt.want {
				t.Errorf("IterationsJSON = %q, want %q", msgs[0].IterationsJSON, tt.want)
			}
		})
	}
}
