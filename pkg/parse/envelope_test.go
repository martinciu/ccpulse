package parse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assistantLine splices env verbatim after "type", so each case states exactly
// which envelope keys the line carries.
func assistantLine(env string) string {
	return `{"type":"assistant",` + env + `"timestamp":"2026-05-09T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20}}}`
}

// parseBoth runs one line through both parse entry points and returns each
// path's result keyed by entry-point name, so a test can hold them to the
// same admission decision.
func parseBoth(t *testing.T, line string) map[string]struct {
	msgs []Message
	errs []ParseError
} {
	t.Helper()
	content := line + "\n"

	msgs, errs, err := ParseWithErrors(strings.NewReader(content), "p")
	if err != nil {
		t.Fatalf("ParseWithErrors err = %v, want nil", err)
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tmsgs, terrs, _, _, err := ParseFromOffsetWithErrors(path, "p", 0, 0)
	if err != nil {
		t.Fatalf("ParseFromOffsetWithErrors err = %v, want nil", err)
	}
	return map[string]struct {
		msgs []Message
		errs []ParseError
	}{
		"ParseWithErrors":           {msgs, errs},
		"ParseFromOffsetWithErrors": {tmsgs, terrs},
	}
}

func TestEnvelopeRequired(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, env, wantMsg string }{ // wantMsg "" = admitted
		{"both present", `"sessionId":"s1","uuid":"u1",`, ""},
		{"sessionId absent", `"uuid":"u1",`, "assistant line has no transcript envelope: missing sessionId"},
		{"sessionId empty", `"sessionId":"","uuid":"u1",`, "assistant line has no transcript envelope: missing sessionId"},
		{"uuid absent", `"sessionId":"s1",`, "assistant line has no transcript envelope: missing uuid"},
		{"uuid empty", `"sessionId":"s1","uuid":"",`, "assistant line has no transcript envelope: missing uuid"},
		{"both absent", ``, "assistant line has no transcript envelope: missing sessionId, uuid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for entry, got := range parseBoth(t, assistantLine(tt.env)) {
				if tt.wantMsg == "" {
					if len(got.msgs) != 1 || len(got.errs) != 0 {
						t.Errorf("%s: got %d msgs, %d errs (%v), want 1 msg, 0 errs", entry, len(got.msgs), len(got.errs), got.errs)
					}
					continue
				}
				if len(got.msgs) != 0 {
					t.Errorf("%s: got %d msgs, want 0 — an envelope-less line must never reach the cache", entry, len(got.msgs))
				}
				if len(got.errs) != 1 {
					t.Fatalf("%s: got %d errs, want 1: %v", entry, len(got.errs), got.errs)
				}
				if err := got.errs[0].Err; !errors.Is(err, ErrMissingEnvelope) || err.Error() != tt.wantMsg {
					t.Errorf("%s: err = %q, want %q wrapping ErrMissingEnvelope", entry, err, tt.wantMsg)
				}
			}
		})
	}
}

// TestEnvelope_Issue527Line pins the exact #527 file shape: it is refused as
// "not a transcript line", not as a bad timestamp.
func TestEnvelope_Issue527Line(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`
	for entry, got := range parseBoth(t, line) {
		if len(got.msgs) != 0 || len(got.errs) != 1 {
			t.Fatalf("%s: got %d msgs, %d errs, want 0 msgs, 1 err", entry, len(got.msgs), len(got.errs))
		}
		err := got.errs[0].Err
		if !errors.Is(err, ErrMissingEnvelope) || errors.Is(err, ErrZeroTimestamp) {
			t.Errorf("%s: err = %v, want ErrMissingEnvelope and not ErrZeroTimestamp", entry, err)
		}
	}
}

// TestEnvelope_CheckedBeforeTimestamp pins the order: "is this a transcript
// line at all?" is asked before "is its timestamp usable?".
func TestEnvelope_CheckedBeforeTimestamp(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","timestamp":"0001-01-01T00:00:00Z","message":{"id":"m1","role":"assistant","model":"claude-opus-5","usage":{"output_tokens":1}}}`
	for entry, got := range parseBoth(t, line) {
		if len(got.errs) != 1 {
			t.Fatalf("%s: got %d errs, want 1", entry, len(got.errs))
		}
		if err := got.errs[0].Err; !errors.Is(err, ErrMissingEnvelope) || errors.Is(err, ErrZeroTimestamp) {
			t.Errorf("%s: err = %v, want ErrMissingEnvelope (checked first), not ErrZeroTimestamp", entry, err)
		}
	}
}

// TestEnvelope_SyntheticAPIErrorLineAdmitted: API-error turns are written with
// the full envelope and a "<synthetic>" model; the envelope rule must not
// refuse them.
func TestEnvelope_SyntheticAPIErrorLineAdmitted(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","sessionId":"s1","uuid":"u1","timestamp":"2026-05-09T10:00:00.000Z","isApiErrorMessage":true,` +
		`"message":{"id":"m1","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"output_tokens":0}}}`
	for entry, got := range parseBoth(t, line) {
		if len(got.errs) != 0 {
			t.Errorf("%s: got errs %v, want none", entry, got.errs)
		}
		if len(got.msgs) != 1 || got.msgs[0].Model != "<synthetic>" {
			t.Errorf("%s: got %+v, want 1 msg with Model <synthetic>", entry, got.msgs)
		}
	}
}
