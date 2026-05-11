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

func TestHeaderShowsResetTime(t *testing.T) {
	// The bars row no longer renders the current % as text (the bar fill
	// itself conveys it); it shows the time-to-reset on the right. This
	// guards that the time appears, and that the label "5h " is still
	// present alongside it.
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "1h 47m") {
		t.Errorf("expected reset time '1h 47m' in:\n%s", got)
	}
	if !strings.Contains(got, "5h") {
		t.Errorf("expected label '5h' in:\n%s", got)
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

func TestBuildChart_NoBaselineStrip(t *testing.T) {
	// Regression for #102: the per-cell ▒/░ baseline strip below the bars
	// was removed; bars fill the full chart height instead. The output's
	// bottom row must contain neither glyph, and its total row count must
	// equal chartH (no extra row of chrome). Scoped to the bottom row so
	// future use of ▒/░ elsewhere (e.g. #96 session-boundary markers)
	// doesn't false-fire.
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

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	bottom := lines[len(lines)-1]
	if strings.Contains(bottom, "▒") {
		t.Errorf("baseline data marker '▒' should be gone from chart bottom row after #102; bottom row was %q in full output:\n%s", bottom, out)
	}
	if strings.Contains(bottom, "░") {
		t.Errorf("baseline gap marker '░' should be gone from chart bottom row after #102; bottom row was %q in full output:\n%s", bottom, out)
	}
	if h := lipgloss.Height(out); h != 10 {
		t.Errorf("buildChart(_, _, 10) should be 10 rows tall; got %d:\n%s", h, out)
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
	// Verifies the two bars sit side-by-side inside the header (not
	// stacked), with both labels and the dim divider present on the
	// same line. The current % is no longer text-rendered — the bar's
	// fill is the visual signal — so this test now keys on labels +
	// divider rather than percent substrings.
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 1, MinutesToReset: 100, Has7d: true, Percent7d: 12, MinutesToReset7d: 1000}
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())
	v := m.View()
	if !strings.Contains(v, " │ ") {
		t.Errorf("expected dim divider ' │ ' in:\n%s", v)
	}
	if !strings.Contains(v, "5h") {
		t.Errorf("expected dim '5h' label prefix in:\n%s", v)
	}
	if !strings.Contains(v, "7d") {
		t.Errorf("expected dim '7d' label prefix in:\n%s", v)
	}

	// Labels and divider must appear on the same line — bars sit
	// side-by-side inside the header box rather than stacked.
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "5h") && strings.Contains(line, "7d") && strings.Contains(line, " │ ") {
			return
		}
	}
	t.Errorf("expected both labels and the divider on the same line; got:\n%s", v)
}

func TestQuotaBarsSymmetric(t *testing.T) {
	// The bars-row produced by quotaBars() must be symmetric across the
	// dim " │ " divider: lipgloss.Width(left) == lipgloss.Width(right).
	// This is the centring property — equivalent to "divider visually
	// centred" but more testable than checking an integer-rounded column
	// index.
	//
	// Cases span the linear regime (60–120 cols, where progressWidth =
	// (W-35)/2) and the clamp regime (40 cols, where progressWidth pins
	// at 6 and the bars-row overflows the box — the symmetry property
	// must still hold inside the overflow).
	cases := []struct {
		name string
		w    int
		win  status.Window
	}{
		{"40cols_clamp", 40, status.Window{Percent: 5, MinutesToReset: 52, Has7d: true, Percent7d: 24, MinutesToReset7d: 8640}},
		{"60cols_short_times", 60, status.Window{Percent: 5, MinutesToReset: 52, Has7d: true, Percent7d: 24, MinutesToReset7d: 8640}},   // 6d
		{"60cols_long_times", 60, status.Window{Percent: 95, MinutesToReset: 299, Has7d: true, Percent7d: 80, MinutesToReset7d: 1439}}, // 4h 59m / 23:59
		{"80cols_short_times", 80, status.Window{Percent: 5, MinutesToReset: 52, Has7d: true, Percent7d: 24, MinutesToReset7d: 8640}},
		{"80cols_long_times", 80, status.Window{Percent: 95, MinutesToReset: 299, Has7d: true, Percent7d: 80, MinutesToReset7d: 1439}},
		{"120cols_zero_times", 120, status.Window{Percent: 0, MinutesToReset: 0, Has7d: true, Percent7d: 0, MinutesToReset7d: 0}},
		{"80cols_no_7d", 80, status.Window{Percent: 5, MinutesToReset: 52, Has7d: false}},
		// Asymmetric Projection cases: one bucket has a Projection, the
		// other is nil. The burn-rate row renders styled rate text on the
		// populated side and "(no data)" on the nil side. Both sides must
		// still produce the same visual width through their respective
		// codepaths (`renderBurnRateSide` for the populated case vs the
		// noData branch for nil).
		{
			"100cols_proj5h_only",
			100,
			status.Window{
				Percent: 43, MinutesToReset: 137,
				Has7d: true, Percent7d: 17, MinutesToReset7d: 7200,
				Projection: &status.Projections{
					FiveHour: &status.Projection{
						SlopePctPerHour:     12,
						ProjectedPctAtReset: 54,
						Confidence:          "ok",
					},
					SevenDay: nil,
				},
			},
		},
		{
			"100cols_proj7d_only",
			100,
			status.Window{
				Percent: 43, MinutesToReset: 137,
				Has7d: true, Percent7d: 17, MinutesToReset7d: 7200,
				Projection: &status.Projections{
					FiveHour: nil,
					SevenDay: &status.Projection{
						SlopePctPerHour:     0.4,
						ProjectedPctAtReset: 70,
						Confidence:          "ok",
					},
				},
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Deps{})
			m.w, m.h = tt.w, 40
			m.window = tt.win
			m.progress = newProgressBar(m.progressWidth())
			m.progress7d = newProgressBar(m.progressWidth())

			bars := m.quotaBars()
			// quotaBars stacks two rows (bars + burn-rate) via JoinVertical
			// — check symmetry on each row independently. Both share the
			// same chrome math, so a width drift in either is a real bug.
			// Split on the full " │ " divider per row rather than the bare
			// │ rune, so the adjacent spaces are excluded from both halves.
			for i, row := range strings.Split(bars, "\n") {
				left, right, ok := strings.Cut(row, " │ ")
				if !ok {
					t.Fatalf("no ' │ ' divider found in quotaBars row %d: %q", i, row)
				}
				lw, rw := lipgloss.Width(left), lipgloss.Width(right)
				if lw != rw {
					t.Errorf("asymmetric quotaBars row %d at w=%d: left width %d, right width %d\nrow: %q", i, tt.w, lw, rw, row)
				}
			}
		})
	}
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


func TestIndexFadeStyle(t *testing.T) {
	// Verifies the three fade stops map to the expected foregrounds.
	// Stop 1 is the default fg (no Foreground set, which lipgloss reports
	// as NoColor{}); stops 2 and 3 step down through the dim Solarized
	// palette already used elsewhere in the TUI.
	cases := []struct {
		stop int
		want lipgloss.TerminalColor
	}{
		{1, lipgloss.NoColor{}},
		{2, Base01},
		{3, Base02},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("stop_%d", c.stop), func(t *testing.T) {
			got := indexFadeStyle(c.stop).GetForeground()
			if got != c.want {
				t.Errorf("indexFadeStyle(%d).GetForeground() = %v, want %v",
					c.stop, got, c.want)
			}
		})
	}
}
