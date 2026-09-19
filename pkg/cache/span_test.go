package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// spanCache opens a temp cache seeded with one message per supplied timestamp.
func spanCache(t *testing.T, stamps ...time.Time) *Cache {
	t.Helper()
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
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

func TestMessageSpanOf_Empty(t *testing.T) {
	t.Parallel()

	span, ok, err := spanCache(t).MessageSpanOf(t.Context())
	if err != nil {
		t.Fatalf("MessageSpanOf: %v", err)
	}
	if ok {
		t.Errorf("ok = true on an empty cache, want false")
	}
	if span.Count != 0 {
		t.Errorf("Count = %d, want 0", span.Count)
	}
	if !span.Earliest.IsZero() || !span.Latest.IsZero() {
		t.Errorf("times = %v/%v, want zero on an empty cache", span.Earliest, span.Latest)
	}
}

func TestMessageSpanOf_SingleRowCollapses(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	span, ok, err := spanCache(t, ts).MessageSpanOf(t.Context())
	if err != nil || !ok {
		t.Fatalf("MessageSpanOf: ok=%v err=%v", ok, err)
	}
	if !span.Earliest.Equal(ts) || !span.Latest.Equal(ts) {
		t.Errorf("span = %v → %v, want both %v", span.Earliest, span.Latest, ts)
	}
	if span.Count != 1 {
		t.Errorf("Count = %d, want 1", span.Count)
	}
}

// TestMessageSpanOf_ZeroTimestampRowIsVisible is the case the span exists for:
// one row at Go's zero time is invisible to integrity_check — the file is
// structurally perfect — but it is what sets the chart's whole x-axis (#527).
// doctor can only report it if MIN(ts) surfaces it.
func TestMessageSpanOf_ZeroTimestampRowIsVisible(t *testing.T) {
	t.Parallel()

	real1 := time.Date(2026, 4, 18, 8, 30, 0, 0, time.UTC)
	real2 := time.Date(2026, 9, 19, 18, 20, 0, 0, time.UTC)
	span, ok, err := spanCache(t, time.Time{}, real1, real2).MessageSpanOf(t.Context())
	if err != nil || !ok {
		t.Fatalf("MessageSpanOf: ok=%v err=%v", ok, err)
	}
	if span.Earliest.Year() > 1 {
		t.Errorf("Earliest = %v, want the year-1 row — MIN(ts) must not hide it", span.Earliest)
	}
	if !span.Latest.Equal(real2) {
		t.Errorf("Latest = %v, want %v", span.Latest, real2)
	}
	if span.Count != 3 {
		t.Errorf("Count = %d, want 3", span.Count)
	}
}

func TestMessageSpanOf_ReturnsUTC(t *testing.T) {
	t.Parallel()

	loc := time.FixedZone("UTC+7", 7*3600)
	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, loc)
	span, ok, err := spanCache(t, ts).MessageSpanOf(t.Context())
	if err != nil || !ok {
		t.Fatalf("MessageSpanOf: ok=%v err=%v", ok, err)
	}
	if span.Earliest.Location() != time.UTC {
		t.Errorf("Earliest location = %v, want UTC", span.Earliest.Location())
	}
	if !span.Earliest.Equal(ts) {
		t.Errorf("Earliest = %v, want the same instant as %v", span.Earliest, ts)
	}
}
