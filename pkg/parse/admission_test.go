package parse

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// admissionGoodLine is an enveloped assistant line that admits as one row.
const admissionGoodLine = `{"type":"assistant","sessionId":"s1","uuid":"u1","timestamp":"2026-05-09T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20}}}`

// admissionAdvisorLine has a message.id and a cross-model advisor iterations
// entry, so it expands into a parent row plus one ":it:" attempt row.
const admissionAdvisorLine = `{"type":"assistant","sessionId":"s1","uuid":"u2","timestamp":"2026-05-09T10:00:01.000Z","message":{"id":"m2","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":2,"output_tokens":300,"iterations":[` +
	`{"input_tokens":1,"output_tokens":100,"cache_read_input_tokens":8000,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"},` +
	`{"input_tokens":150970,"output_tokens":6758,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"advisor_message","model":"claude-opus-4-7"},` +
	`{"input_tokens":1,"output_tokens":200,"cache_read_input_tokens":12000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":400},"type":"message"}]}}}`

func writeAdmissionFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanAdmission(t *testing.T) {
	t.Parallel()

	lines := []string{
		admissionGoodLine,    // 1 admitted
		admissionAdvisorLine, // 2 admitted once, despite expanding to two rows
		`{"type":"user","sessionId":"s1","uuid":"u3","message":{"role":"user","content":"hi"}}`,                                                           // 3 ignored
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`,                                                      // 4 refused: no envelope (#527)
		`{"type":"assistant","sessionId":"s1","uuid":"u5","timestamp":"0001-01-01T00:00:00Z","message":{"role":"assistant","usage":{"output_tokens":1}}}`, // 5 refused: zero timestamp
		`{"type":"assistant","sessionId":"s1","uuid":"u6","timestamp":"2026-05-09T10:00:00Z","message":{"usage":{"input_tokens":"10"}}}`,                  // 6 refused: undecodable
		`this is not json at all`, // 7 neither
	}
	// 8: a good line with no trailing newline — an in-progress tail, neither.
	content := strings.Join(lines, "\n") + "\n" + admissionGoodLine
	path := writeAdmissionFile(t, content)

	got, err := ScanAdmission(path)
	if err != nil {
		t.Fatalf("ScanAdmission err = %v, want nil", err)
	}
	if want := (Admission{Admitted: 2, Refused: 3}); got != want {
		t.Errorf("ScanAdmission = %+v, want %+v", got, want)
	}

	// Line 2 really expanded: without this, Admitted == 2 would not prove
	// that iteration rows are kept out of the denominator.
	msgs, _, _, _, err := ParseFromOffsetWithErrors(path, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Errorf("ParseFromOffsetWithErrors yielded %d messages, want 3 (2 parents + 1 attempt row)", len(msgs))
	}
}

// TestScanAdmission_OversizedLineNeitherCounted: an oversized line is skipped
// by the scanner, not decided on, and the lines after it still count. Not
// parallel: withScannerMaxBytes mutates a package global.
func TestScanAdmission_OversizedLineNeitherCounted(t *testing.T) {
	withScannerMaxBytes(t)

	big := `{"type":"assistant","sessionId":"s1","uuid":"u9","padding":"` + strings.Repeat("x", 5000) + `"}`
	path := writeAdmissionFile(t, admissionGoodLine+"\n"+big+"\n"+admissionGoodLine+"\n")

	got, err := ScanAdmission(path)
	if err != nil {
		t.Fatalf("ScanAdmission err = %v, want nil", err)
	}
	if want := (Admission{Admitted: 2, Refused: 0}); got != want {
		t.Errorf("ScanAdmission = %+v, want %+v", got, want)
	}
}

func TestScanAdmission_MissingFile(t *testing.T) {
	t.Parallel()

	got, err := ScanAdmission(filepath.Join(t.TempDir(), "absent.jsonl"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
	if got != (Admission{}) {
		t.Errorf("Admission = %+v, want zero on error", got)
	}
}
