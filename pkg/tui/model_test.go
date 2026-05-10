package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/status"
)

func TestInitialView_RendersHeader(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	v := m.View()
	if !strings.Contains(v, "╭") {
		t.Errorf("expected box border '╭' in view, got:\n%s", v)
	}
}

func TestHeaderShowsPercent(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "61%") {
		t.Errorf("expected 61%% in:\n%s", got)
	}
	if !strings.Contains(got, "1h 47m") {
		t.Errorf("expected 1h 47m in:\n%s", got)
	}
}

func TestHelpToggle(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	v := updated.(Model).View()
	// help overlay renders key binding descriptions
	if !strings.Contains(v, "scroll") {
		t.Errorf("help overlay missing key descriptions: %s", v)
	}
}

func TestZoomCycles(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	initial := m.zoomIdx
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	next := updated.(Model).zoomIdx
	if next == initial {
		t.Errorf("zoom did not change: still %d", next)
	}
	// Three z presses should cycle back to start
	m2 := updated.(Model)
	r2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m2 = r2.(Model)
	r3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if r3.(Model).zoomIdx != initial {
		t.Errorf("zoom did not cycle: want %d, got %d", initial, r3.(Model).zoomIdx)
	}
}

func TestHelpModeSuppressesScroll(t *testing.T) {
	// While the help overlay is up, scroll keys must not affect the
	// underlying chart's offset — otherwise dismissing help (?) reveals
	// a different position than the one the user left.
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	// Seed the viewport with content wider than its width so scroll has
	// room to actually move.
	m.viewport.SetContent(strings.Repeat("X", 500))
	m.viewport.SetXOffset(50)
	startPct := m.viewport.HorizontalScrollPercent()

	// Toggle help on.
	r1, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = r1.(Model)
	if !m.showHelp {
		t.Fatalf("expected showHelp=true after '?'")
	}

	// Mash scroll keys.
	for _, k := range []rune{'l', 'l', 'h', 'l'} {
		r, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = r.(Model)
	}
	if m.viewport.HorizontalScrollPercent() != startPct {
		t.Errorf("scroll keys leaked through help overlay: scroll%% went %.3f → %.3f",
			startPct, m.viewport.HorizontalScrollPercent())
	}

	// Zoom must also not change.
	startZoom := m.zoomIdx
	rz, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = rz.(Model)
	if m.zoomIdx != startZoom {
		t.Errorf("zoom key leaked through help overlay: %d → %d",
			startZoom, m.zoomIdx)
	}
}

func TestQuitKey(t *testing.T) {
	m := New(Deps{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected quit cmd, got nil")
	}
}

func TestRefreshChart_AllEmptyShowsBaseline(t *testing.T) {
	c, err := cache.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Seed stale content to confirm refresh clears it.
	m.viewport.SetContent("STALE CHART CONTENT")

	m.refreshChart()

	got := m.viewport.View()
	if strings.Contains(got, "STALE CHART CONTENT") {
		t.Errorf("refreshChart left stale content in viewport:\n%s", got)
	}
	// New behaviour (issue #53): an empty cache renders the placeholder
	// text rather than a fake baseline.
	if !strings.Contains(got, "no Claude sessions yet") {
		t.Errorf("expected placeholder text 'no Claude sessions yet' in:\n%s", got)
	}
}

func TestBuildChartEmitsBars(t *testing.T) {
	// Regression: when chartW == len(buckets), the default ntcharts BarGap
	// of 1 forces bar width to (graphSize - gaps) / numBars = 0, so no bars
	// render. buildChart must pass WithBarGap(0) and a Style that has both
	// Foreground and Background set, otherwise the chart is just blank
	// canvas + axis line.
	now := time.Now()
	buckets := make([]cache.TokenBucket, 30)
	for i := range buckets {
		buckets[i] = cache.TokenBucket{
			BucketStart: now.Add(time.Duration(i) * 15 * time.Minute),
			Tokens:      int64((i*7 + 1000) * (1 + i%3)),
		}
	}
	out := buildChart(buckets, 30, 10)
	if !strings.ContainsAny(out, "█▇▆▅▄▃▂▁") {
		t.Errorf("buildChart produced no bar block characters; got:\n%s", out)
	}
}

func TestBuildChart_BaselineRow(t *testing.T) {
	// Mixed buckets: some empty (gap) and some non-empty (data).
	now := time.Now()
	buckets := make([]cache.TokenBucket, 20)
	for i := range buckets {
		buckets[i] = cache.TokenBucket{BucketStart: now.Add(time.Duration(i) * 5 * time.Minute)}
	}
	// Indices 5..9 carry data; everything else is a gap.
	for i := 5; i < 10; i++ {
		buckets[i].Tokens = int64((i + 1) * 1000)
	}
	out := buildChart(buckets, 20, 10)

	if !strings.Contains(out, "▒") {
		t.Errorf("baseline row missing data marker '▒' in:\n%s", out)
	}
	if !strings.Contains(out, "░") {
		t.Errorf("baseline row missing gap marker '░' in:\n%s", out)
	}

	// Bars must still render — Task 4 must not regress TestBuildChartEmitsBars.
	if !strings.ContainsAny(out, "█▇▆▅▄▃▂▁") {
		t.Errorf("buildChart produced no bar block characters; got:\n%s", out)
	}
}

func TestHeatColor(t *testing.T) {
	tests := []struct {
		ratio float64
		want  lipgloss.Color
	}{
		{0.0, Green},
		{0.32, Green},
		{0.33, Yellow},
		{0.65, Yellow},
		{0.66, Red},
		{1.0, Red},
	}
	for _, tt := range tests {
		got := heatColor(tt.ratio)
		if got != tt.want {
			t.Errorf("heatColor(%v) = %v, want %v", tt.ratio, got, tt.want)
		}
	}
}

func TestIndexProgressMsg(t *testing.T) {
	m := New(Deps{})
	updated, _ := m.Update(IndexProgressMsg{Done: 3, Total: 7, Active: true})
	got := updated.(Model)
	if got.indexDone != 3 || got.indexTotal != 7 || !got.indexActive {
		t.Errorf("IndexProgressMsg not applied: %+v", got)
	}
}

func TestSevenDayBarRendered(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 1, Has7d: true, Percent7d: 12}
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())
	v := m.View()
	if !strings.Contains(v, "  1%") {
		t.Errorf("expected 5h percent '  1%%' in:\n%s", v)
	}
	if !strings.Contains(v, " 12%") {
		t.Errorf("expected 7d percent ' 12%%' in:\n%s", v)
	}
	if !strings.Contains(v, " │ ") {
		t.Errorf("expected dim divider ' │ ' in:\n%s", v)
	}

	// Both percents and the divider must appear on the same line — bars
	// sit side-by-side inside the header box rather than stacked.
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "  1%") && strings.Contains(line, " 12%") && strings.Contains(line, " │ ") {
			return
		}
	}
	t.Errorf("expected both percents and the divider on the same line; got:\n%s", v)
}

func TestSevenDayBarPlaceholderWhenAbsent(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 1, Has7d: false}
	m.progress = newProgressBar(m.progressWidth())
	v := m.View()
	if !strings.Contains(v, "no data") {
		t.Errorf("expected 'no data' placeholder in:\n%s", v)
	}
}

func TestQuotaBarRendered(t *testing.T) {
	// progress.ViewAs at a non-zero percent emits the solid-fill ANSI
	// background sequence for at least one block. View() at 0% emits only
	// the unfilled track; at 80% it must emit filled blocks. Compare.
	low := New(Deps{})
	low.w, low.h = 120, 40
	low.window = status.Window{Percent: 0}
	low.progress = newProgressBar(low.progressWidth())

	hi := New(Deps{})
	hi.w, hi.h = 120, 40
	hi.window = status.Window{Percent: 80}
	hi.progress = newProgressBar(hi.progressWidth())

	if low.View() == hi.View() {
		t.Errorf("quota bar must render differently at 0%% vs 80%%; got identical View output")
	}
}

func TestViewFitsTerminal(t *testing.T) {
	for _, h := range []int{20, 40, 60} {
		m := New(Deps{})
		m.w, m.h = 120, h
		m.viewport.Width = m.chartWidth()
		m.viewport.Height = m.chartHeight()
		m.viewport.SetContent(strings.Repeat("X\n", m.chartHeight()))
		v := m.View()
		got := strings.Count(v, "\n") + 1
		if got > h {
			t.Errorf("h=%d: rendered %d lines, exceeds terminal height", h, got)
		}
	}
}

func TestRefreshChart_FromEarliest(t *testing.T) {
	// After issue #53, refreshChart must load from the earliest cached
	// message (aligned to the active zoom's bucket boundary) up to "now".
	// We verify this by inserting a single message ~3 hours ago and
	// confirming TokenBuckets returns at least the matching number of
	// 15m buckets at zoom index 1 (15m bucket / no Lookback).
	c, err := cache.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now()
	earliest := now.Add(-3 * time.Hour)
	if err := c.InsertMessages([]parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   earliest,
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}

	zoom := ZoomLevels[1] // 15m
	to := cache.BucketAlign(now, zoom.Duration).Add(zoom.Duration)
	from := cache.BucketAlign(earliest, zoom.Duration)
	wantMin := int(to.Sub(from)/zoom.Duration) - 1 // tolerate ±1 bucket on boundary

	buckets, err := c.TokenBuckets(zoom.Duration, from, to)
	if err != nil {
		t.Fatalf("TokenBuckets: %v", err)
	}
	if len(buckets) < wantMin {
		t.Errorf("got %d buckets, want at least %d (≈3h at 15m)", len(buckets), wantMin)
	}

	// And refreshChart on the model must not panic and must populate
	// the viewport with bar content (not the empty-cache placeholder).
	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.zoomIdx = 1
	m.refreshChart()
	got := m.viewport.View()
	if strings.Contains(got, "no Claude sessions yet") {
		t.Errorf("refreshChart rendered empty-state placeholder for non-empty cache:\n%s", got)
	}
}

func TestQuotaMsgApplied(t *testing.T) {
	m := New(Deps{Cache: nil})
	msg := QuotaMsg{
		Usage:     &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: time.Now().Add(time.Hour)}},
		Source:    "api",
		UpdatedAt: time.Now(),
	}
	next, _ := m.Update(msg)
	nm := next.(Model)
	if nm.quota == nil || nm.quotaSource != "api" {
		t.Errorf("QuotaMsg not applied: %+v", nm)
	}
}

func TestHeaderShowsDevChip(t *testing.T) {
	m := New(Deps{IsDev: true})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 5, MinutesToReset: 60, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "[DEV]") {
		t.Errorf("expected [DEV] chip in dev header, got:\n%s", got)
	}
	// [DEV] now lives on the footer line, side-by-side with keybindings.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "[DEV]") && strings.Contains(line, "q quit") {
			return
		}
	}
	t.Errorf("expected [DEV] on the same line as 'q quit' (footer); got:\n%s", got)
}

func TestFooterRightAlignsIndicators(t *testing.T) {
	m := New(Deps{IsDev: true})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 5, MinutesToReset: 60}
	updated, _ := m.Update(IndexProgressMsg{Done: 12, Total: 30, Active: true})
	m = updated.(Model)
	v := m.View()

	var footer string
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "q quit") {
			footer = line
			break
		}
	}
	if footer == "" {
		t.Fatalf("footer line (containing 'q quit') not found in:\n%s", v)
	}
	if !strings.Contains(footer, "indexing 12/30") {
		t.Errorf("footer missing 'indexing 12/30': %q", footer)
	}
	if !strings.Contains(footer, "[DEV]") {
		t.Errorf("footer missing '[DEV]': %q", footer)
	}
	if strings.Index(footer, "indexing") >= strings.Index(footer, "[DEV]") {
		t.Errorf("expected 'indexing' before '[DEV]' (DEV rightmost): %q", footer)
	}
}

func TestHeaderHidesDevChipInRelease(t *testing.T) {
	m := New(Deps{}) // IsDev defaults to false
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 5, MinutesToReset: 60, CeilingLabel: "max_20x"}
	got := m.View()
	if strings.Contains(got, "[DEV]") {
		t.Errorf("release header should not contain [DEV] chip:\n%s", got)
	}
}

func TestRenderIndicators(t *testing.T) {
	now := time.Now()
	stale := status.Window{QuotaSource: "cache_stale", QuotaUpdatedAt: now.Add(-5 * time.Minute)}
	tests := []struct {
		name      string
		isDev     bool
		idx       IndexProgress
		w         status.Window
		wantParts []string
		wantEmpty bool
	}{
		{"all idle", false, IndexProgress{}, status.Window{}, nil, true},
		{"dev only", true, IndexProgress{}, status.Window{}, []string{"[DEV]"}, false},
		{"indexing only", false, IndexProgress{Active: true, Done: 12, Total: 30}, status.Window{}, []string{"indexing 12/30"}, false},
		{"stale only", false, IndexProgress{}, stale, []string{"⚠ 5m old"}, false},
		{"all active", true, IndexProgress{Active: true, Done: 1, Total: 2}, stale, []string{"⚠ 5m old", "indexing 1/2", "[DEV]"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderIndicators(tt.isDev, tt.idx, tt.w)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			for _, p := range tt.wantParts {
				if !strings.Contains(got, p) {
					t.Errorf("missing %q in %q", p, got)
				}
			}
			if tt.name == "all active" {
				stIdx := strings.Index(got, "⚠")
				ixIdx := strings.Index(got, "indexing")
				devIdx := strings.Index(got, "[DEV]")
				if !(stIdx < ixIdx && ixIdx < devIdx) {
					t.Errorf("expected stale < indexing < [DEV] order, got %q", got)
				}
			}
		})
	}
}

func TestFormatReset7d(t *testing.T) {
	tests := []struct {
		mins int
		want string
	}{
		{30, "00:30"},
		{90, "01:30"},
		{1439, "23:59"},
		{1440, "1d"},
		{1500, "1d"}, // truncates, does not round
		{10080, "7d"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dmins", tt.mins), func(t *testing.T) {
			got := formatReset7d(tt.mins)
			if got != tt.want {
				t.Errorf("formatReset7d(%d) = %q, want %q", tt.mins, got, tt.want)
			}
		})
	}
}
