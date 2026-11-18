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

// TestReportStatuslineFeed covers the doctor line for the statusline overlay
// (#568). Informational throughout, like the backoff line: a missing feed is
// a setup hint, and a corrupt file is ignored by every reader. Raw JSON
// fixtures pin the on-disk contract `status --stdin` writes.
func TestReportStatuslineFeed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	bucket := func(resets, asOf time.Duration) string {
		return fmt.Sprintf(`{"utilization":54,"resets_at":%q,"as_of":%q,"accepted_at":%q}`,
			stamp(resets), stamp(asOf), stamp(asOf))
	}

	tests := []struct {
		name     string
		body     string // "" means: write no file at all
		wantText string
		notText  []string
	}{
		{
			name:     "no file",
			wantText: "ℹ statusline feed: none yet — add --stdin to the ccpulse status call in your statusline (see README)\n",
		},
		{
			name:     "recent reading",
			body:     `{"v":1,"seven_day":` + bucket(72*time.Hour, -(2*time.Minute+3*time.Second)) + `}`,
			wantText: "ℹ statusline feed: last reading 2m3s ago\n",
		},
		{
			name: "newest as_of across buckets",
			body: `{"v":1,"five_hour":` + bucket(time.Hour, -10*time.Second) +
				`,"seven_day":` + bucket(72*time.Hour, -time.Hour) + `}`,
			wantText: "ℹ statusline feed: last reading 10s ago\n",
		},
		{
			// The feed's liveness is about readings arriving, not about
			// whether their windows are still open.
			name:     "expired buckets still count",
			body:     `{"v":1,"seven_day":` + bucket(-time.Minute, -3*time.Hour) + `}`,
			wantText: "ℹ statusline feed: last reading 3h0m0s ago\n",
		},
		{
			name:     "file without an accepted reading",
			body:     `{"v":1,"sessions":{"S1":{"seven_day":{"utilization":54,"resets_at":"2026-11-17T09:00:00Z"},"seen_at":"2026-11-14T11:00:00Z"}}}`,
			wantText: "ℹ statusline feed: payloads seen, no reading accepted yet\n",
			notText:  []string{"add --stdin"},
		},
		{
			name:     "corrupt file",
			body:     `{garbage`,
			wantText: "ℹ statusline feed: state file unusable, ignored — ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tt.body != "" {
				if err := os.WriteFile(filepath.Join(dir, "usage-statusline.json"), []byte(tt.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			reportStatuslineFeed(&buf, dir, now)
			got := buf.String()
			if !strings.HasPrefix(got, tt.wantText) {
				t.Errorf("output = %q, want prefix %q", got, tt.wantText)
			}
			for _, unwanted := range tt.notText {
				if strings.Contains(got, unwanted) {
					t.Errorf("output must not contain %q, got %q", unwanted, got)
				}
			}
			if strings.Count(got, "\n") != 1 {
				t.Errorf("want exactly one line, got %q", got)
			}
		})
	}
}
