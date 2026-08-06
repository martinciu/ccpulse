package parse

import (
	"bufio"
	"encoding/json"
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
		wantMsgs   int    // expected message count after expansion (#456); 0 means 1
	}{
		{name: "absent", iterations: "", want: ""},
		{name: "json null", iterations: "null", want: ""},
		{name: "empty array", iterations: "[]", want: ""},
		{name: "single redundant", iterations: "[" + redundantEntry + "]", want: ""},
		{
			name:       "single redundant with model equal to outer",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-fable-5"}]`,
			want:       "",
		},
		{
			name:       "single with differing model kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-opus-4-8"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-opus-4-8"}]`,
			wantMsgs:   2, // foreign entry expands into an attempt row (#456)
		},
		{
			name:       "single non-message type kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"fallback_message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"fallback_message"}]`,
		},
		{
			name:       "single token mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":299,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":299,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name:       "single input mismatch kept",
			iterations: `[{"input_tokens":3,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want:       `[{"input_tokens":3,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name:       "single cache_read mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20001,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20001,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name:       "single cache_creation_input_tokens mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":401,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":401,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name:       "single ephemeral_5m mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
		},
		{
			name:       "single ephemeral_1h mismatch kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":401},"type":"message"}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":401},"type":"message"}]`,
		},
		{
			name:       "single entry with type absent kept",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400}}]`,
			want:       `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400}}]`,
		},
		{
			name:       "single redundant plus unknown field dropped",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","duration_ms":123}]`,
			want:       "",
		},
		{
			name:       "multi-attempt kept",
			iterations: "[" + redundantEntry + `,{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":21000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]`,
			want:       "[" + redundantEntry + `,{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":21000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]`,
			wantMsgs:   2, // foreign fallback entry expands into an attempt row (#456)
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
			wantN := tt.wantMsgs
			if wantN == 0 {
				wantN = 1
			}
			if len(msgs) != wantN {
				t.Fatalf("got %d messages, want %d", len(msgs), wantN)
			}
			if msgs[0].IterationsJSON != tt.want {
				t.Errorf("IterationsJSON = %q, want %q", msgs[0].IterationsJSON, tt.want)
			}
		})
	}
}

// TestIterationsOversizedKeptWithoutProbe asserts that an iterations blob
// larger than maxIterationsProbe is returned verbatim without ever being
// decoded. The fixture is a single entry matching the outer usage on all
// known fields — probing it would classify it redundant and return "" — so
// only the size guard's pre-probe short-circuit can produce the verbatim
// result asserted here.
func TestIterationsOversizedKeptWithoutProbe(t *testing.T) {
	padding := strings.Repeat("x", maxIterationsProbe)
	its := `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","padding":"` + padding + `"}]`

	if len(its) <= maxIterationsProbe {
		t.Fatalf("fixture too small: len=%d, want > %d", len(its), maxIterationsProbe)
	}

	var raw rawLine
	raw.Message.Model = "claude-fable-5"
	raw.Message.Usage.InputTokens = 2
	raw.Message.Usage.OutputTokens = 300
	raw.Message.Usage.CacheReadInputTokens = 20000
	raw.Message.Usage.CacheCreationInputTokens = 400
	raw.Message.Usage.CacheCreation.Ephemeral1hInputTokens = 400
	raw.Message.Usage.Iterations = json.RawMessage(its)

	got := informativeIterations(raw)
	if got != its {
		t.Errorf("informativeIterations returned %d bytes, want verbatim input of %d bytes", len(got), len(its))
	}
}

// TestIterationsOversizedKeptWithoutExpansion asserts the same size guard end
// to end through Parse: an iterations array over maxIterationsProbe bytes
// must never reach toMessages' expansion path. Parse should yield exactly the
// parent message, outer usage untouched, with IterationsJSON holding the
// oversized blob verbatim.
func TestIterationsOversizedKeptWithoutExpansion(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[{"input_tokens":1}`)
	for b.Len() <= maxIterationsProbe {
		b.WriteString(`,{"input_tokens":1}`)
	}
	b.WriteByte(']')
	its := b.String()

	if len(its) <= maxIterationsProbe {
		t.Fatalf("fixture too small: len=%d, want > %d", len(its), maxIterationsProbe)
	}

	line := fmt.Sprintf(`{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"iterations":%s}}}`, its) + "\n"

	msgs, err := Parse(strings.NewReader(line), "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (oversized blob must skip expansion)", len(msgs))
	}
	if msgs[0].InputTokens != 2 || msgs[0].OutputTokens != 300 || msgs[0].CacheReadTokens != 20000 ||
		msgs[0].CacheWrite5mTokens != 0 || msgs[0].CacheWrite1hTokens != 400 {
		t.Errorf("outer usage = (in=%d out=%d cacheRead=%d cw5m=%d cw1h=%d), want (in=2 out=300 cacheRead=20000 cw5m=0 cw1h=400)",
			msgs[0].InputTokens, msgs[0].OutputTokens, msgs[0].CacheReadTokens, msgs[0].CacheWrite5mTokens, msgs[0].CacheWrite1hTokens)
	}
	if msgs[0].IterationsJSON != its {
		t.Errorf("IterationsJSON len=%d, want verbatim blob of len=%d", len(msgs[0].IterationsJSON), len(its))
	}
}

// TestIterationsExpansion drives full JSONL lines through Parse and asserts
// the attempt-row expansion: parent tokens become the sum of own-model
// entries, and each non-zero foreign-model entry becomes an extra Message
// keyed "<id>:it:<idx>". Shapes are lifted from real cache blobs (issue #456).
func TestIterationsExpansion(t *testing.T) {
	type wantMsg struct {
		messageID  string
		model      string
		in, out    int64
		cacheRead  int64
		cw5m, cw1h int64
	}
	tests := []struct {
		name       string
		iterations string // raw JSON for the iterations value; "" = omit the key
		want       []wantMsg
	}{
		{
			name: "fallback: refused foreign attempt plus serving fallback_message",
			iterations: `[` +
				`{"input_tokens":2,"output_tokens":434,"cache_read_input_tokens":686578,"cache_creation_input_tokens":62,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":62},"type":"message","model":"claude-opus-4-8"},` +
				`{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"fallback_message","model":"claude-fable-5"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
				{messageID: "m1:it:0", model: "claude-opus-4-8", in: 2, out: 434, cacheRead: 686578, cw1h: 62},
			},
		},
		{
			name: "advisor: cross-model advisor between two own message entries",
			iterations: `[` +
				`{"input_tokens":1,"output_tokens":100,"cache_read_input_tokens":8000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"},` +
				`{"input_tokens":150970,"output_tokens":6758,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"advisor_message","model":"claude-opus-4-7"},` +
				`{"input_tokens":1,"output_tokens":200,"cache_read_input_tokens":12000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
				{messageID: "m1:it:1", model: "claude-opus-4-7", in: 150970, out: 6758},
			},
		},
		{
			name: "advisor with serving model folds into parent, no attempt row",
			iterations: `[` +
				`{"input_tokens":1,"output_tokens":100,"cache_read_input_tokens":8000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"},` +
				`{"input_tokens":131674,"output_tokens":4632,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"advisor_message","model":"claude-fable-5"},` +
				`{"input_tokens":1,"output_tokens":200,"cache_read_input_tokens":12000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 131676, out: 4932, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name:       "degenerate single own entry upgrades near-zeroed outer usage",
			iterations: `[{"input_tokens":2,"output_tokens":261,"cache_read_input_tokens":997513,"cache_creation_input_tokens":464,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":464},"type":"message"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 261, cacheRead: 997513, cw1h: 464},
			},
		},
		{
			name:       "single foreign entry moves all usage to its model",
			iterations: `[{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message","model":"claude-opus-4-8"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5"},
				{messageID: "m1:it:0", model: "claude-opus-4-8", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name: "all-zero foreign entry skipped",
			iterations: `[` +
				`{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"},` +
				`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-opus-4-8"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name:       "fail-open unparseable blob keeps outer usage, no expansion",
			iterations: `{"unexpected":"object"}`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name:       "all-zero single foreign entry keeps outer usage",
			iterations: `[{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-opus-4-8"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name:       "all-zero single own entry keeps outer usage",
			iterations: `[{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
			},
		},
		{
			name: "cache-only foreign entry still becomes attempt row",
			iterations: `[` +
				`{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"},` +
				`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":50000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-opus-4-8"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
				{messageID: "m1:it:1", model: "claude-opus-4-8", cacheRead: 50000},
			},
		},
		{
			name: "two foreign entries keep positional keys with a gap",
			iterations: `[` +
				`{"input_tokens":2,"output_tokens":434,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-opus-4-8"},` +
				`{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"},` +
				`{"input_tokens":1,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-opus-4-7"}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 300, cacheRead: 20000, cw1h: 400},
				{messageID: "m1:it:0", model: "claude-opus-4-8", in: 2, out: 434},
				{messageID: "m1:it:2", model: "claude-opus-4-7", in: 1, out: 50},
			},
		},
		{
			name:       "explicit empty-string model classified as own",
			iterations: `[{"input_tokens":2,"output_tokens":261,"cache_read_input_tokens":997513,"cache_creation_input_tokens":464,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":464},"type":"message","model":""}]`,
			want: []wantMsg{
				{messageID: "m1", model: "claude-fable-5", in: 2, out: 261, cacheRead: 997513, cw1h: 464},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iterField := ""
			if tt.iterations != "" {
				iterField = `,"iterations":` + tt.iterations
			}
			line := fmt.Sprintf(`{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","effort":"high","message":{"id":"m1","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":2,"output_tokens":300,"cache_read_input_tokens":20000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400}%s}}}`, iterField) + "\n"

			msgs, err := Parse(strings.NewReader(line), "test-slug")
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != len(tt.want) {
				t.Fatalf("got %d messages, want %d: %+v", len(msgs), len(tt.want), msgs)
			}
			if msgs[0].IterationsJSON != tt.iterations {
				t.Errorf("msgs[0].IterationsJSON = %q, want %q (verbatim parent blob)", msgs[0].IterationsJSON, tt.iterations)
			}
			for i, w := range tt.want {
				g := msgs[i]
				if g.MessageID != w.messageID || g.Model != w.model {
					t.Errorf("msg[%d] = (%q, %q), want (%q, %q)", i, g.MessageID, g.Model, w.messageID, w.model)
				}
				if g.InputTokens != w.in || g.OutputTokens != w.out || g.CacheReadTokens != w.cacheRead ||
					g.CacheWrite5mTokens != w.cw5m || g.CacheWrite1hTokens != w.cw1h {
					t.Errorf("msg[%d] tokens = (in=%d out=%d cacheRead=%d cw5m=%d cw1h=%d), want (in=%d out=%d cacheRead=%d cw5m=%d cw1h=%d)", i,
						g.InputTokens, g.OutputTokens, g.CacheReadTokens, g.CacheWrite5mTokens, g.CacheWrite1hTokens,
						w.in, w.out, w.cacheRead, w.cw5m, w.cw1h)
				}
				if g.SessionID != "s1" || g.Effort != "high" || g.ProjectSlug != "test-slug" {
					t.Errorf("msg[%d] envelope not inherited: session=%q effort=%q slug=%q", i, g.SessionID, g.Effort, g.ProjectSlug)
				}
				if i > 0 && g.IterationsJSON != "" {
					t.Errorf("msg[%d] attempt row must not carry IterationsJSON, got %q", i, g.IterationsJSON)
				}
			}
		})
	}
}

// noMessageIDIterations is the informative iterations array used by
// TestIterationsExpansion_NoMessageID: one foreign-model entry plus one
// serving-model fallback entry, neither of which restates the outer usage.
const noMessageIDIterations = `[{"input_tokens":2,"output_tokens":434,"type":"message","model":"claude-opus-4-8"},{"input_tokens":2,"output_tokens":300,"type":"fallback_message","model":"claude-fable-5"}]`

// TestIterationsExpansion_NoMessageID asserts expansion is skipped entirely
// when the line has no message.id — a bare ":it:<idx>" key would collide
// across turns in a session.
func TestIterationsExpansion_NoMessageID(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"role":"assistant","model":"claude-fable-5","usage":{"input_tokens":2,"output_tokens":300,"iterations":` + noMessageIDIterations + `}}}` + "\n"

	msgs, err := Parse(strings.NewReader(line), "test-slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (no expansion without message.id)", len(msgs))
	}
	if msgs[0].InputTokens != 2 || msgs[0].OutputTokens != 300 {
		t.Errorf("parent tokens = (%d,%d), want outer (2,300)", msgs[0].InputTokens, msgs[0].OutputTokens)
	}
	if msgs[0].IterationsJSON != noMessageIDIterations {
		t.Errorf("IterationsJSON = %q, want %q (verbatim blob)", msgs[0].IterationsJSON, noMessageIDIterations)
	}
}
