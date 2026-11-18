package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/status"
)

func TestAgeString(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{3*time.Hour + 10*time.Minute, "3h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{24 * time.Hour, "1d"},
		{50 * time.Hour, "2d"},
	} {
		if got := ageString(tt.d); got != tt.want {
			t.Errorf("ageString(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestScopedRowLabel(t *testing.T) {
	t.Parallel()
	if got, want := scopedRowLabel("Fable", 59*time.Minute), scopedLabel("Fable"); got != want {
		t.Errorf("59m: %q, want %q (no suffix while the API is healthy)", got, want)
	}
	if got, want := scopedRowLabel("Fable", scopedStaleAfter), scopedLabel("Fable"); got != want {
		t.Errorf("exactly scopedStaleAfter: %q, want %q", got, want)
	}
	if got, want := scopedRowLabel("Fable", 3*time.Hour), "Fable · 3h old "; got != want {
		t.Errorf("3h: %q, want %q", got, want)
	}
	if got := scopedRowLabel("Fable", 0); got != scopedLabel("Fable") {
		t.Errorf("zero age (no API data): %q, want no suffix", got)
	}
}

func TestRenderIndicators_StatuslineBadge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		w    status.Window
		want string // "" → no badge
	}{
		{"statusline 11m old", status.Window{QuotaSource: anthro.SourceStatusline, QuotaUpdatedAt: now.Add(-11 * time.Minute)}, "⚠ 11m old"},
		{"statusline 9m old", status.Window{QuotaSource: anthro.SourceStatusline, QuotaUpdatedAt: now.Add(-9 * time.Minute)}, ""},
		{"cache_stale unchanged", status.Window{QuotaSource: "cache_stale", QuotaUpdatedAt: now.Add(-5 * time.Minute)}, "⚠ 5m old"},
		{"cache_fresh never", status.Window{QuotaSource: "cache_fresh", QuotaUpdatedAt: now.Add(-20 * time.Minute)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderIndicators(false, IndexProgress{}, tt.w)
			if tt.want == "" {
				if strings.Contains(got, "old") {
					t.Errorf("unexpected badge %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("badge = %q, want %q", got, tt.want)
			}
		})
	}
}

// scopedRowAfterQuota feeds a QuotaMsg through Update on a cache-backed
// model (recomputeWindow needs the DB) and returns the first scoped row.
func scopedRowAfterQuota(t *testing.T, apiUpdatedAt time.Time) string {
	t.Helper()
	m := seedIntroModel(t, true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	updated, _ = m.Update(QuotaMsg{
		Usage:        quotaUsageScoped(80, 25, "Fable"),
		Source:       anthro.SourceStatusline,
		UpdatedAt:    time.Now(),
		APIUpdatedAt: apiUpdatedAt,
	})
	m = updated.(Model)
	rows := strings.Split(m.quotaBars(), "\n")
	if len(rows) < 3 {
		t.Fatalf("quotaBars rows = %d, want a scoped row", len(rows))
	}
	return rows[2]
}

// Not parallel: withForcedColor mutates the global renderer.
func TestScopedRowAgeSuffix(t *testing.T) {
	withForcedColor(t)
	t.Run("API data 3h old", func(t *testing.T) {
		row := scopedRowAfterQuota(t, time.Now().Add(-3*time.Hour))
		if plain := stripANSI(row); !strings.Contains(plain, "Fable · 3h old") {
			t.Fatalf("scoped row %q lacks the age suffix", plain)
		}
		// Line-count probes strip ANSI; the suffix must sit inside the
		// dim-styled label run, not render in the default foreground.
		if dim := dimStyle.Render(scopedRowLabel("Fable", 3*time.Hour)); !strings.Contains(row, dim) {
			t.Errorf("suffix not inside the dim label run:\nrow %q\nwant substring %q", row, dim)
		}
	})
	t.Run("API data 10m old", func(t *testing.T) {
		row := scopedRowAfterQuota(t, time.Now().Add(-10*time.Minute))
		if plain := stripANSI(row); strings.Contains(plain, " old") {
			t.Errorf("scoped row %q shows an age suffix while the API is healthy", plain)
		}
	})
	t.Run("no API data", func(t *testing.T) {
		row := scopedRowAfterQuota(t, time.Time{})
		if plain := stripANSI(row); strings.Contains(plain, " old") {
			t.Errorf("scoped row %q shows an age suffix with no API age", plain)
		}
	})
}

func TestViewFitsWithStaleScopedRow(t *testing.T) {
	t.Parallel()
	for _, w := range []int{40, 44, 60, 80, 100, 120, 121, 160, 200} {
		m := newScopedTestModel(t, w, 40, 2)
		m.window.APIUpdatedAt = time.Now().Add(-50 * time.Hour)
		m.scopedAPIAge = 50 * time.Hour
		m.rebuildScopedBars()
		inner := w - 4
		rows := strings.Split(m.quotaBars(), "\n")
		if !strings.Contains(stripANSI(rows[2]), "· 2d old") {
			t.Fatalf("w=%d: scoped row %q lacks the suffix (test setup)", w, stripANSI(rows[2]))
		}
		for i, line := range rows {
			if lw := lipgloss.Width(line); lw > inner {
				t.Errorf("w=%d row %d: width %d exceeds inner %d\n%q", w, i, lw, inner, line)
			}
		}
	}
	// View-level: the frame still fits m.h, and the suffix never widens the
	// frame. Absolute width is not checkable here: this bare model's unbuilt
	// chart body already renders wider than m.w with no suffix, and View
	// pads every line to its widest — so compare against the same model
	// without the suffix instead.
	maxWidth := func(v string) int {
		w := 0
		for line := range strings.SplitSeq(v, "\n") {
			w = max(w, lipgloss.Width(line))
		}
		return w
	}
	for _, h := range []int{24, 30, 40} {
		build := func(age time.Duration) Model {
			m := newScopedTestModel(t, 100, h, 2)
			m.scopedAPIAge = age
			m.rebuildScopedBars()
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			return m
		}
		fresh, stale := build(0), build(50*time.Hour)
		assertFits(t, stale)
		if got, base := maxWidth(stale.View()), maxWidth(fresh.View()); got > base {
			t.Errorf("h=%d: stale suffix widens the frame to %d (without it: %d)", h, got, base)
		}
	}
}

// A QuotaMsg with nil Usage (the poller's "every bucket expired" signal)
// drops the quota and the scoped rows, and the frame still fits the
// terminal after the header shrinks back.
func TestQuotaMsg_NilUsageClearsQuota(t *testing.T) {
	m := seedIntroModel(t, true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	updated, _ = m.Update(QuotaMsg{
		Usage:     quotaUsageScoped(70, 35, "Fable"),
		Source:    anthro.SourceStatusline,
		UpdatedAt: time.Now(),
	})
	m = updated.(Model)
	if m.scopedRowCount() == 0 {
		t.Fatal("setup: expected a scoped row")
	}
	updated, _ = m.Update(QuotaMsg{})
	m = updated.(Model)
	if m.quota != nil || m.scopedRowCount() != 0 {
		t.Errorf("after nil-Usage QuotaMsg: quota=%v scopedRows=%d, want nil and 0", m.quota, m.scopedRowCount())
	}
	assertFits(t, m)
}
