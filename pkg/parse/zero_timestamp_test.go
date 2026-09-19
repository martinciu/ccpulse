package parse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zeroTSLines are the assistant-line shapes that must never become a stored
// message because they carry no placeable timestamp (#527).
//
// They are refused at two different depths, which is why wantSentinel varies:
// an ABSENT or year-1 `timestamp` decodes cleanly to the zero time.Time and is
// caught by assistantMessages, whereas an EMPTY string fails inside
// time.Time's own UnmarshalJSON, so the line is already rejected as a malformed
// JSON line before the guard is reached. Both outcomes are correct and both are
// reported; the invariant this table pins is that none of them yields a
// Message, and none of them passes silently.
var zeroTSLines = []struct {
	name         string
	line         string
	wantSentinel error // nil: rejected earlier, at JSON decode
}{
	{
		name:         "timestamp key absent",
		line:         `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}`,
		wantSentinel: ErrZeroTimestamp,
	},
	{
		name:         "timestamp spelled as year 1",
		line:         `{"type":"assistant","timestamp":"0001-01-01T00:00:00.000Z","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}`,
		wantSentinel: ErrZeroTimestamp,
	},
	{
		name: "timestamp empty string",
		line: `{"type":"assistant","timestamp":"","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2}}}`,
	},
}

const goodLine = `{"type":"assistant","timestamp":"2026-05-09T10:00:00.000Z","sessionId":"s1","message":{"id":"m1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20}}}`

func TestParseWithErrors_ZeroTimestampSkipped(t *testing.T) {
	t.Parallel()

	for _, tt := range zeroTSLines {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msgs, errs, err := ParseWithErrors(strings.NewReader(tt.line+"\n"), "slug")
			if err != nil {
				t.Fatalf("ParseWithErrors returned err = %v, want nil", err)
			}
			if len(msgs) != 0 {
				t.Errorf("got %d messages, want 0 — a zero-timestamp line must never reach the cache", len(msgs))
			}
			if len(errs) != 1 {
				t.Fatalf("got %d parse errors, want 1 (the skip must be reported, not silent)", len(errs))
			}
			if tt.wantSentinel != nil && !errors.Is(errs[0].Err, tt.wantSentinel) {
				t.Errorf("errs[0].Err = %v, want it to wrap %v", errs[0].Err, tt.wantSentinel)
			}
			if errs[0].Line != 1 {
				t.Errorf("errs[0].Line = %d, want 1", errs[0].Line)
			}
		})
	}
}

// TestParseWithErrors_ZeroTimestampDoesNotAbortFile pins the recovery
// behaviour: one unusable line must cost exactly that line, not the rest of
// the transcript after it.
func TestParseWithErrors_ZeroTimestampDoesNotAbortFile(t *testing.T) {
	t.Parallel()

	in := zeroTSLines[0].line + "\n" + goodLine + "\n"
	msgs, errs, err := ParseWithErrors(strings.NewReader(in), "slug")
	if err != nil {
		t.Fatalf("ParseWithErrors returned err = %v, want nil", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (the good line after the bad one)", len(msgs))
	}
	if msgs[0].MessageID != "m1" {
		t.Errorf("MessageID = %q, want %q", msgs[0].MessageID, "m1")
	}
	if len(errs) != 1 || !errors.Is(errs[0].Err, ErrZeroTimestamp) {
		t.Errorf("errs = %v, want exactly one ErrZeroTimestamp", errs)
	}
	if errs[0].Line != 1 {
		t.Errorf("errs[0].Line = %d, want 1", errs[0].Line)
	}
}

// TestParseFromOffsetWithErrors_ZeroTimestampSkipped covers the incremental
// tail path. The guard lives in one helper precisely so this path and
// ParseWithErrors cannot drift apart — this test is what holds that true.
func TestParseFromOffsetWithErrors_ZeroTimestampSkipped(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "poisoned.jsonl")
	if err := os.WriteFile(path, []byte(zeroTSLines[0].line+"\n"+goodLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	msgs, errs, _, line, err := ParseFromOffsetWithErrors(path, "slug", 0, 0)
	if err != nil {
		t.Fatalf("ParseFromOffsetWithErrors returned err = %v, want nil", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].MessageID != "m1" {
		t.Errorf("MessageID = %q, want %q", msgs[0].MessageID, "m1")
	}
	if len(errs) != 1 || !errors.Is(errs[0].Err, ErrZeroTimestamp) {
		t.Fatalf("errs = %v, want exactly one ErrZeroTimestamp", errs)
	}
	if errs[0].Line != 1 {
		t.Errorf("errs[0].Line = %d, want 1", errs[0].Line)
	}
	// The cursor must still advance past both lines; a skipped line is
	// consumed, not left for the next fs event to re-read forever.
	if line != 2 {
		t.Errorf("line = %d, want 2", line)
	}
}
