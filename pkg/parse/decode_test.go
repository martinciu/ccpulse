package parse

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// typeChangedLine is a real-shaped assistant line whose usage.input_tokens
// changed JSON type (number → string). It fails the whole-line decode, which
// is exactly the format change ErrUndecodableAssistant exists to surface.
const typeChangedLine = `{"type":"assistant","sessionId":"s1","uuid":"u1","timestamp":"2026-05-09T10:00:00Z","message":{"usage":{"input_tokens":"10"}}}`

func TestDecodeLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		line            string
		wantErr         bool
		wantUndecodable bool
	}{
		{"valid assistant", `{"type":"assistant","sessionId":"s1","uuid":"u1","timestamp":"2026-05-09T10:00:00Z","message":{"usage":{"input_tokens":10}}}`, false, false},
		{"input_tokens as string", typeChangedLine, true, true},
		{"empty timestamp", `{"type":"assistant","sessionId":"s1","uuid":"u1","timestamp":""}`, true, true},
		{"sessionId as number", `{"type":"assistant","sessionId":42,"uuid":"u1","timestamp":"2026-05-09T10:00:00Z"}`, true, true},
		{"truncated assistant", `{"type":"assistant","message":{"role":"assi`, true, false},
		{"not json", `this is not json at all`, true, false},
		{"user line type mismatch", `{"type":"user","timestamp":""}`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeLine([]byte(tt.line))
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeLine err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := errors.Is(err, ErrUndecodableAssistant); got != tt.wantUndecodable {
				t.Errorf("errors.Is(err, ErrUndecodableAssistant) = %v, want %v (err = %v)", got, tt.wantUndecodable, err)
			}
		})
	}
}

// TestDecodeLine_KeepsCause pins that the classification wraps the original
// decode error rather than replacing it: both verbs are %w, so the concrete
// json error stays reachable for anyone diagnosing the line.
func TestDecodeLine_KeepsCause(t *testing.T) {
	t.Parallel()

	_, err := decodeLine([]byte(typeChangedLine))
	if !errors.Is(err, ErrUndecodableAssistant) {
		t.Fatalf("err = %v, want it to wrap ErrUndecodableAssistant", err)
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("errors.As(err, *json.UnmarshalTypeError) = false, want true (cause dropped from chain): %v", err)
	}
}

// TestUndecodableAssistant_BothEntryPoints holds the two parse entry points to
// one classification: decodeLine is shared precisely so they cannot drift.
func TestUndecodableAssistant_BothEntryPoints(t *testing.T) {
	t.Parallel()

	content := typeChangedLine + "\n"

	t.Run("ParseWithErrors", func(t *testing.T) {
		t.Parallel()

		msgs, errs, err := ParseWithErrors(strings.NewReader(content), "p")
		if err != nil {
			t.Fatalf("ParseWithErrors err = %v, want nil", err)
		}
		assertOneUndecodable(t, msgs, errs)
	})

	t.Run("ParseFromOffsetWithErrors", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "t.jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		msgs, errs, _, _, err := ParseFromOffsetWithErrors(path, "p", 0, 0)
		if err != nil {
			t.Fatalf("ParseFromOffsetWithErrors err = %v, want nil", err)
		}
		assertOneUndecodable(t, msgs, errs)
	})
}

func assertOneUndecodable(t *testing.T, msgs []Message, errs []ParseError) {
	t.Helper()
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d parse errors, want 1: %v", len(errs), errs)
	}
	if errs[0].Line != 1 {
		t.Errorf("errs[0].Line = %d, want 1", errs[0].Line)
	}
	if !errors.Is(errs[0].Err, ErrUndecodableAssistant) {
		t.Errorf("errs[0].Err = %v, want it to wrap ErrUndecodableAssistant", errs[0].Err)
	}
}
