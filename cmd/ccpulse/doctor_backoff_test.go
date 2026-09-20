package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReportBackoffState covers the doctor line for the second state file in
// the cache dir (#529). It is informational throughout: an open window is
// ccpulse behaving as designed after a 429, and even a corrupt file is not a
// fault — Fetch ignores it and keeps working. Only doctor says so out loud,
// which is why the unreadable case gets its own branch.
//
// The fixtures are raw JSON on purpose: this is the on-disk contract between
// the TUI and every short-lived `ccpulse status`, so the test pins the
// literal shape rather than round-tripping through the writer.
func TestReportBackoffState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string {
		return now.Add(d).Format(time.RFC3339Nano)
	}

	tests := []struct {
		name     string
		body     string // "" means: write no file at all
		wantText []string
		notText  []string
	}{
		{
			name:     "absent",
			wantText: []string{"ℹ usage API backoff: none"},
		},
		{
			name:     "drained window",
			body:     fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":2}`, stamp(-time.Minute)),
			wantText: []string{"ℹ usage API backoff: none"},
		},
		{
			name:     "open window",
			body:     fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":3}`, stamp(12*time.Minute)),
			wantText: []string{"ℹ usage API backoff: retry in 12m0s (3 consecutive 429s)"},
		},
		{
			// A transport failure or a 5xx opens a window too, and carries
			// no 429s at all. "0 consecutive 429s" beside a live deadline
			// reads as a contradiction.
			name:     "a window with no 429s behind it names its cause",
			body:     fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":0}`, stamp(3*time.Minute)),
			wantText: []string{"retry in 3m0s (last attempt failed; not rate-limited)"},
			notText:  []string{"429"},
		},
		{
			name:     "a single 429 reads as singular",
			body:     fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":1}`, stamp(6*time.Minute)),
			wantText: []string{"(1 consecutive 429)"},
			notText:  []string{"429s"},
		},
		{
			name:     "unreadable state file",
			body:     "not json",
			wantText: []string{"ℹ usage API backoff: state file unusable, ignored"},
		},
		{
			// The value that once bricked the app: doctor must not present a
			// discarded deadline as an active window.
			name:     "retry_at beyond the ceiling",
			body:     `{"v":1,"retry_at":"3000-01-01T00:00:00Z","consecutive_429":3}`,
			wantText: []string{"state file unusable, ignored", "ceiling"},
			notText:  []string{"retry in"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.body != "" {
				if err := os.WriteFile(filepath.Join(dir, "usage-backoff.json"), []byte(tt.body), 0o600); err != nil {
					t.Fatalf("seed backoff file: %v", err)
				}
			}

			var buf bytes.Buffer
			reportBackoffState(&buf, dir, now)
			got := buf.String()

			for _, want := range tt.wantText {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q, got:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.notText {
				if strings.Contains(got, unwanted) {
					t.Errorf("output must not contain %q, got:\n%s", unwanted, got)
				}
			}
			if strings.Contains(got, "✗") {
				t.Errorf("the backoff line is informational and must never report a fault, got:\n%s", got)
			}
		})
	}
}
