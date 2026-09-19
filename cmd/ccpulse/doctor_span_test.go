package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// spanDoctorCache opens a temp cache seeded with one message per timestamp.
func spanDoctorCache(t *testing.T, stamps ...time.Time) *cache.Cache {
	t.Helper()
	c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if len(stamps) == 0 {
		return c
	}
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	msgs := make([]parse.Message, 0, len(stamps))
	for i, ts := range stamps {
		msgs = append(msgs, parse.Message{
			SessionID: "s", MessageID: "m" + time.Duration(i).String(),
			ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts, InputTokens: 10, OutputTokens: 5,
		})
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	return c
}

// TestReportMessageSpan_FlagsImplausibleOldestRow is the regression guard for
// the diagnostic gap behind #527: every doctor line reported ✓ while the TUI was
// unbootable, because the database file really was structurally perfect. The
// span check is the one that has to notice.
func TestReportMessageSpan_FlagsImplausibleOldestRow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		stamps   []time.Time
		wantMark string
		wantText []string
	}{
		{
			name:     "year 1 row",
			stamps:   []time.Time{{}, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)},
			wantMark: "✗",
			wantText: []string{"0001-01-01", "predates Claude Code", "ccpulse index --rebuild"},
		},
		{
			// The Unix epoch is the OTHER value a missing timestamp decays to,
			// and layer 1 in pkg/parse deliberately admits it as a valid instant.
			name:     "unix epoch row",
			stamps:   []time.Time{time.Unix(0, 0).UTC(), time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)},
			wantMark: "✗",
			wantText: []string{"1970-01-01", "predates Claude Code"},
		},
		{
			name: "ordinary history",
			stamps: []time.Time{
				time.Date(2026, 4, 18, 8, 30, 0, 0, time.UTC),
				time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
			},
			wantMark: "✓",
			wantText: []string{"cache span: 2026-04-18 → 2026-09-19 (2 messages)", "message timestamps plausible"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			reportMessageSpan(t.Context(), &buf, spanDoctorCache(t, tt.stamps...))
			got := buf.String()

			if !strings.Contains(got, tt.wantMark+" message timestamps plausible") {
				t.Errorf("want a %q verdict on the timestamps line, got:\n%s", tt.wantMark, got)
			}
			for _, want := range tt.wantText {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q, got:\n%s", want, got)
				}
			}
		})
	}
}

// TestReportMessageSpan_EmptyCache: a first launch is not a fault, and must not
// render as one.
func TestReportMessageSpan_EmptyCache(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	reportMessageSpan(t.Context(), &buf, spanDoctorCache(t))
	got := buf.String()

	if !strings.Contains(got, "ℹ cache: no messages indexed yet") {
		t.Errorf("want the informational empty-cache line, got:\n%s", got)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("an empty cache must not report a failure, got:\n%s", got)
	}
}

// TestImplausibleBefore_AdmitsRealHistory pins the floor from the other side: it
// must never grade a genuine transcript as corrupt. Claude Code shipped in 2025,
// so 2020 leaves years of headroom while still catching year 1 and the epoch.
func TestImplausibleBefore_AdmitsRealHistory(t *testing.T) {
	t.Parallel()

	if implausibleBefore.After(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("implausibleBefore = %v is late enough to flag real transcripts as corrupt",
			implausibleBefore)
	}
	if !implausibleBefore.After(time.Unix(0, 0).UTC()) {
		t.Errorf("implausibleBefore = %v does not catch the Unix epoch", implausibleBefore)
	}
}
