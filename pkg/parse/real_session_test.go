package parse

// testdata/real_session.jsonl is a short excerpt of one real Claude Code
// 2.1.288 session: a file-history-snapshot, a user prompt, assistant
// thinking / tool_use / text lines, the matching tool_result, an assistant
// line whose usage.iterations bills a cross-model advisor, a system
// turn_duration line, and (last) an assistant line from the session's
// subagent transcript. Every key is kept; every value is scrubbed — ids map to
// "fx-<n>" (links preserved), cwd to /Users/x/proj, gitBranch to main, all
// free text to "redacted".
//
// What this guards: the admission rule against regression, and a record of
// the envelope ccpulse depends on. What it cannot do: detect a FUTURE Claude
// Code format change — CI only ever sees this committed file. `ccpulse
// doctor`'s transcript-admission line does that, against live transcripts.

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

const realSessionPath = "testdata/real_session.jsonl"

// realSessionLines returns the fixture's lines, without trailing newlines.
func realSessionLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(realSessionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// firstRealAssistantLine decodes the fixture's first assistant line into a
// generic map, so tests can mutate exactly one field of a real shape.
func firstRealAssistantLine(t *testing.T) map[string]any {
	t.Helper()
	for _, l := range realSessionLines(t) {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["type"] == "assistant" {
			return m
		}
	}
	t.Fatal("fixture has no assistant line")
	return nil
}

func parseMutated(t *testing.T, m map[string]any) ([]Message, []ParseError) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	msgs, errs, err := ParseWithErrors(strings.NewReader(string(b)+"\n"), "p")
	if err != nil {
		t.Fatalf("ParseWithErrors err = %v, want nil", err)
	}
	return msgs, errs
}

func TestRealSession_AllAssistantLinesAdmitted(t *testing.T) {
	t.Parallel()

	assistantLines := 0
	for _, l := range realSessionLines(t) {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(l), &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Type == "assistant" {
			assistantLines++
		}
	}

	f, err := os.Open(realSessionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	msgs, errs, err := ParseWithErrors(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("got %d parse errors, want 0: %v", len(errs), errs)
	}

	parents, attempts := 0, 0
	for _, m := range msgs {
		if strings.Contains(m.MessageID, attemptKeySep) {
			attempts++
		} else {
			parents++
		}
	}
	if parents != assistantLines {
		t.Errorf("parent rows = %d, want %d (one per assistant line)", parents, assistantLines)
	}
	if attempts == 0 {
		t.Error("no attempt (\":it:\") rows: the cross-model advisor line did not expand")
	}
}

func TestRealSession_EnvelopeMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{"sessionId deleted", func(m map[string]any) { delete(m, "sessionId") }},
		{"sessionId empty", func(m map[string]any) { m["sessionId"] = "" }},
		{"uuid deleted", func(m map[string]any) { delete(m, "uuid") }},
		{"uuid empty", func(m map[string]any) { m["uuid"] = "" }},
		{"both deleted", func(m map[string]any) { delete(m, "sessionId"); delete(m, "uuid") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := firstRealAssistantLine(t)
			tt.mutate(m)
			msgs, errs := parseMutated(t, m)
			if len(msgs) != 0 {
				t.Errorf("got %d msgs, want 0", len(msgs))
			}
			if len(errs) != 1 || !errors.Is(errs[0].Err, ErrMissingEnvelope) {
				t.Errorf("errs = %v, want exactly one ErrMissingEnvelope", errs)
			}
		})
	}
}

func TestRealSession_TypeChangeUndecodable(t *testing.T) {
	t.Parallel()

	m := firstRealAssistantLine(t)
	msg, _ := m["message"].(map[string]any)
	usage, _ := msg["usage"].(map[string]any)
	if usage == nil {
		t.Fatal("fixture's first assistant line has no message.usage")
	}
	usage["input_tokens"] = "1"

	msgs, errs := parseMutated(t, m)
	if len(msgs) != 0 {
		t.Errorf("got %d msgs, want 0", len(msgs))
	}
	if len(errs) != 1 || !errors.Is(errs[0].Err, ErrUndecodableAssistant) {
		t.Errorf("errs = %v, want exactly one ErrUndecodableAssistant", errs)
	}
}
