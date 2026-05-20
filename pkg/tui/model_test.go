package tui

import (
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
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

// intPtr returns &n. Test-only helper for the *int Window.MinutesToReset
// fields, post #189 (5h-idle and 7d-glitch need to be expressible as nil).
func intPtr(n int) *int { return &n }

// timePtr returns &t. Test-only helper for the *time.Time
// anthro.Bucket.ResetsAt field on sites that build the value inline.
func timePtr(t time.Time) *time.Time { return &t }

// BenchmarkModelView measures the per-frame cost of the full View()
// composition: header + sep + viewport + sep + footer. View() runs on
// every keypress and tick — regressions here (e.g. an extra lipgloss
// style allocation) bleed into perceived input latency. Sub-benches
// cover wide and narrow terminal widths so a regression that only
// manifests at one canvas size doesn't slip past a single-dimension
// bench. After #132 both widths share the same code path (the Y axis
// is now an in-canvas overlay, not a separate JoinHorizontal column).
func BenchmarkModelView(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		b.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		b.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := make([]parse.Message, 200)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: int64(1000 + i*100),
		}
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		b.Fatalf("InsertMessages: %v", err)
	}

	cases := []struct {
		name string
		w, h int
	}{
		{"wide", 120, 40},
		{"narrow", 20, 40},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			m := New(Deps{Cache: c})
			m.w, m.h = tt.w, tt.h
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			m.refreshChart()

			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkView = m.View()
			}
		})
	}
}

// sinkView prevents the compiler from eliding the View call in
// BenchmarkModelView when its return value is otherwise unused.
var sinkView string

func TestChartWidth_FloorsAtTen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		w    int
		want int
	}{
		{"wide", 120, 118},
		{"narrow but not floored", 20, 18},
		{"floored at 10", 8, 10},
		{"floored at 10 when w==12", 12, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := Model{w: tt.w}
			if got := m.chartWidth(); got != tt.want {
				t.Errorf("chartWidth() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRefreshChart_CachesPeak(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7", Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7", Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	if m.peak == 0 {
		t.Errorf("expected non-zero peak after insert, got %v", m.peak)
	}
}

func TestView_YLabelFixedAcrossScroll(t *testing.T) {
	// The Y label is overlaid on the post-scroll viewport output, so it
	// must appear in View() both at the default scroll-to-now position
	// AND after the user scrolls left/right. Anything else means the
	// label tracks the canvas (#132 bug) instead of the viewport.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC()
	msgs := make([]parse.Message, 200)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: int64(1000 + i*100),
		}
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 30
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	expected := formatUnitValue(niceCeilingFloat(m.peak), chartUnit(m.unitIdx))
	if expected == "" || expected == "0" {
		t.Fatalf("expected non-empty Y label; m.peak = %v", m.peak)
	}
	if !strings.Contains(m.View(), expected) {
		t.Errorf("View output missing Y label %q at default scroll position:\n%s", expected, m.View())
	}

	// Capture the default-position peak before scrolling. The windowed
	// rescale (#255) must update m.peak as the user scrolls into older,
	// higher-token buckets — so the post-scroll peak must differ from this
	// captured value, proving live rescale rather than a stale snapshot.
	peakAtDefault := m.peak

	// Scroll a few steps left and right; a Y label must still be overlaid on
	// the viewport's left edge (#132). Windowing rescales the peak live
	// (#255), so recompute the expected label from the post-scroll peak —
	// the test's point is that the label is present and pinned to the
	// viewport, not that the peak is constant.
	for range 5 {
		m.scrollLeft(horizontalScrollStep)
	}
	// Value-correctness: scrolling left into older (higher-input) buckets must
	// change m.peak, proving live windowed rescale. This assertion does NOT
	// derive its expectation from m.peak, so it is non-tautological.
	if m.peak == peakAtDefault {
		t.Errorf("expected m.peak to change after ScrollLeft (windowed rescale #255); still %v", m.peak)
	}
	expected = formatUnitValue(niceCeilingFloat(m.peak), chartUnit(m.unitIdx))
	if !strings.Contains(m.View(), expected) {
		t.Errorf("View output missing Y label %q after ScrollLeft (label should be fixed to viewport):\n%s",
			expected, m.View())
	}
	for range 3 {
		m.scrollRight(horizontalScrollStep)
	}
	expected = formatUnitValue(niceCeilingFloat(m.peak), chartUnit(m.unitIdx))
	if !strings.Contains(m.View(), expected) {
		t.Errorf("View output missing Y label %q after ScrollRight:\n%s", expected, m.View())
	}
}

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
	m.window = status.Window{Percent: 61, MinutesToReset: intPtr(107), CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "1h 47m") {
		t.Errorf("expected reset time '1h 47m' in:\n%s", got)
	}
	if !strings.Contains(got, "5h") {
		t.Errorf("expected label '5h' in:\n%s", got)
	}
}

func TestQuotaBarsRendersIdleForNil5hResetsAt(t *testing.T) {
	// Post #189: a 5h Window with MinutesToReset == nil means the
	// rolling window is idle (Anthropic reports resets_at: null when no
	// turns have landed in the last 5h). The header should render
	// "idle" on the 5h side rather than a misleading "0m".
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{
		Percent:          0,
		MinutesToReset:   nil,
		Has7d:            true,
		Percent7d:        50,
		MinutesToReset7d: intPtr(60 * 24),
	}
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())

	out := m.quotaBars()
	if !strings.Contains(out, "idle") {
		t.Errorf("expected 'idle' in 5h side, got:\n%s", out)
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
	// room to actually move. Also seed lastStarts so setX's clamp lets
	// the requested offset through (setX clamps against len(lastStarts) -
	// chartWidth).
	m.viewport.SetContent(strings.Repeat("X", 500))
	m.lastStarts = make([]time.Time, 500)
	m.setX(50)
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
	values, starts, peak := projectBuckets(buckets)
	out := buildChart(values, starts, peak, 30, 10, now, ZoomLevels[1], chartUnitTokens, dateOrderMonthFirst)
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
	values, starts, peak := projectBuckets(buckets)
	out := buildChart(values, starts, peak, 20, 10, now, ZoomLevels[0], chartUnitTokens, dateOrderMonthFirst)

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

// TestBuildChart_BarColorByUnit pins issue #162: bars render in the
// unit-keyed AdaptiveColor (Blue for tokens, Amber for cost), independent
// of bucket height. We probe by rendering the chart with each unit and
// asserting the unit's Light hex appears in the ANSI-stripped style escape,
// and the OTHER unit's hex does not.
func TestBuildChart_BarColorByUnit(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, false) // probe Light stops: #1565c0 and #ff8f00
	values := []float64{1, 2, 3, 4, 5}
	starts := make([]time.Time, len(values))
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	for i := range starts {
		starts[i] = now.Add(time.Duration(i) * time.Hour)
	}
	peak := 5.0

	// lipgloss renders AdaptiveColor as truecolor SGR escapes; the hex
	// digits appear (as decimal RGB triplets) inside the escape sequence.
	// Probe with the Light stops since the test process has no TTY and
	// termenv reports "no color preference" → lipgloss picks Light.
	tokensRGB := "21;101;192" // #1565c0 → 0x15=21, 0x65=101, 0xc0=192
	costRGB := "255;143;0"    // #ff8f00 → 255, 143, 0

	tokensOut := buildChart(values, starts, peak, 30, 10, now, ZoomLevels[1], chartUnitTokens, dateOrderMonthFirst)
	if !strings.Contains(tokensOut, tokensRGB) {
		t.Errorf("chartUnitTokens render missing Blue RGB %q in output:\n%s", tokensRGB, tokensOut)
	}
	if strings.Contains(tokensOut, costRGB) {
		t.Errorf("chartUnitTokens render unexpectedly contains Amber RGB %q (should be unit-keyed, not heat-ramped):\n%s", costRGB, tokensOut)
	}

	costOut := buildChart(values, starts, peak, 30, 10, now, ZoomLevels[1], chartUnitCost, dateOrderMonthFirst)
	if !strings.Contains(costOut, costRGB) {
		t.Errorf("chartUnitCost render missing Amber RGB %q in output:\n%s", costRGB, costOut)
	}
	if strings.Contains(costOut, tokensRGB) {
		t.Errorf("chartUnitCost render unexpectedly contains Blue RGB %q:\n%s", tokensRGB, costOut)
	}
}

func TestIndexProgressMsg(t *testing.T) {
	// Drives Model.Update through the four message-edge cases. The
	// falling edge (true → false) is the only one that should schedule
	// a tea.Tick and bump indexFadeStop to 1.
	cases := []struct {
		name          string
		priorActive   bool
		priorFadeStop int
		msg           IndexProgressMsg
		wantActive    bool
		wantFadeStop  int
		wantDone      int
		wantTotal     int
		wantTickCmd   bool
	}{
		{
			name:        "rising_edge_no_fade",
			priorActive: false, priorFadeStop: 0,
			msg:        IndexProgressMsg{Done: 0, Total: 5, Active: true},
			wantActive: true, wantFadeStop: 0,
			wantDone: 0, wantTotal: 5, wantTickCmd: false,
		},
		{
			name:        "active_to_active_no_fade",
			priorActive: true, priorFadeStop: 0,
			msg:        IndexProgressMsg{Done: 3, Total: 5, Active: true},
			wantActive: true, wantFadeStop: 0,
			wantDone: 3, wantTotal: 5, wantTickCmd: false,
		},
		{
			name:        "falling_edge_starts_fade",
			priorActive: true, priorFadeStop: 0,
			msg:        IndexProgressMsg{Done: 5, Total: 5, Active: false},
			wantActive: false, wantFadeStop: 1,
			wantDone: 5, wantTotal: 5, wantTickCmd: true,
		},
		{
			name:        "active_clears_existing_fade",
			priorActive: false, priorFadeStop: 2,
			msg:        IndexProgressMsg{Done: 0, Total: 5, Active: true},
			wantActive: true, wantFadeStop: 0,
			wantDone: 0, wantTotal: 5, wantTickCmd: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(Deps{})
			m.indexLastActive = c.priorActive
			m.indexActive = c.priorActive
			m.indexFadeStop = c.priorFadeStop

			updated, cmd := m.Update(c.msg)
			got := updated.(Model)

			if got.indexActive != c.wantActive {
				t.Errorf("indexActive: got %v, want %v", got.indexActive, c.wantActive)
			}
			if got.indexFadeStop != c.wantFadeStop {
				t.Errorf("indexFadeStop: got %d, want %d", got.indexFadeStop, c.wantFadeStop)
			}
			if got.indexDone != c.wantDone {
				t.Errorf("indexDone: got %d, want %d", got.indexDone, c.wantDone)
			}
			if got.indexTotal != c.wantTotal {
				t.Errorf("indexTotal: got %d, want %d", got.indexTotal, c.wantTotal)
			}
			if (cmd != nil) != c.wantTickCmd {
				t.Errorf("tea.Cmd presence: got %v, want %v", cmd != nil, c.wantTickCmd)
			}
		})
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
	m.window = status.Window{Percent: 1, MinutesToReset: intPtr(100), Has7d: true, Percent7d: 12, MinutesToReset7d: intPtr(1000)}
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
		{"40cols_clamp", 40, status.Window{Percent: 5, MinutesToReset: intPtr(52), Has7d: true, Percent7d: 24, MinutesToReset7d: intPtr(8640)}},
		{"60cols_short_times", 60, status.Window{Percent: 5, MinutesToReset: intPtr(52), Has7d: true, Percent7d: 24, MinutesToReset7d: intPtr(8640)}},   // 6d
		{"60cols_long_times", 60, status.Window{Percent: 95, MinutesToReset: intPtr(299), Has7d: true, Percent7d: 80, MinutesToReset7d: intPtr(1439)}}, // 4h 59m / 23:59
		{"80cols_short_times", 80, status.Window{Percent: 5, MinutesToReset: intPtr(52), Has7d: true, Percent7d: 24, MinutesToReset7d: intPtr(8640)}},
		{"80cols_long_times", 80, status.Window{Percent: 95, MinutesToReset: intPtr(299), Has7d: true, Percent7d: 80, MinutesToReset7d: intPtr(1439)}},
		{"120cols_zero_times", 120, status.Window{Percent: 0, MinutesToReset: intPtr(0), Has7d: true, Percent7d: 0, MinutesToReset7d: intPtr(0)}},
		{"80cols_no_7d", 80, status.Window{Percent: 5, MinutesToReset: intPtr(52), Has7d: false}},
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
				Percent: 43, MinutesToReset: intPtr(137),
				Has7d: true, Percent7d: 17, MinutesToReset7d: intPtr(7200),
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
				Percent: 43, MinutesToReset: intPtr(137),
				Has7d: true, Percent7d: 17, MinutesToReset7d: intPtr(7200),
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
	// confirming IOTokenBuckets returns at least the matching number of
	// 15m buckets at zoom index 0 (15m zoom, the default).
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

	zoom := ZoomLevels[0] // 15m
	to := cache.BucketAlign(now, zoom.Duration).Add(zoom.Duration)
	from := cache.BucketAlign(earliest, zoom.Duration)
	wantMin := int(to.Sub(from)/zoom.Duration) - 1 // tolerate ±1 bucket on boundary

	buckets, err := c.IOTokenBuckets(zoom.Duration, from, to)
	if err != nil {
		t.Fatalf("IOTokenBuckets: %v", err)
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
		Usage:     &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: timePtr(time.Now().Add(time.Hour))}},
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
	m.window = status.Window{Percent: 5, MinutesToReset: intPtr(60), CeilingLabel: "max_20x"}
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
	m.window = status.Window{Percent: 5, MinutesToReset: intPtr(60)}
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
	m.window = status.Window{Percent: 5, MinutesToReset: intPtr(60), CeilingLabel: "max_20x"}
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
		{"fade stop 1", false, IndexProgress{FadeStop: 1, Done: 100}, status.Window{}, []string{"✓ indexed 100"}, false},
		{"fade stop 2", false, IndexProgress{FadeStop: 2, Done: 100}, status.Window{}, []string{"✓ indexed 100"}, false},
		{"fade stop 3", false, IndexProgress{FadeStop: 3, Done: 100}, status.Window{}, []string{"✓ indexed 100"}, false},
		{"fade stop 0 emits nothing", false, IndexProgress{FadeStop: 0, Done: 100}, status.Window{}, nil, true},
		{"active wins over fade", false, IndexProgress{Active: true, FadeStop: 2, Done: 5, Total: 10}, status.Window{}, []string{"indexing 5/10"}, false},
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
			if tt.name == "active wins over fade" {
				if strings.Contains(got, "✓ indexed") {
					t.Errorf("active should suppress fade output; got %q", got)
				}
			}
		})
	}
}

func TestTickFadeMsg(t *testing.T) {
	// Drives the tick handler through the three stops, the final
	// cleanup, and a stale-tick guard.
	cases := []struct {
		name          string
		priorFadeStop int
		wantFadeStop  int
		wantTickCmd   bool
	}{
		{"stale_tick_when_idle", 0, 0, false},
		{"stop1_to_stop2", 1, 2, true},
		{"stop2_to_stop3", 2, 3, true},
		{"stop3_clears_to_idle", 3, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(Deps{})
			m.indexFadeStop = c.priorFadeStop

			updated, cmd := m.Update(tickFadeMsg{})
			got := updated.(Model)

			if got.indexFadeStop != c.wantFadeStop {
				t.Errorf("indexFadeStop: got %d, want %d", got.indexFadeStop, c.wantFadeStop)
			}
			if (cmd != nil) != c.wantTickCmd {
				t.Errorf("tea.Cmd presence: got %v, want %v", cmd != nil, c.wantTickCmd)
			}
		})
	}
}

func TestIndexProgressMsg_ReduceMotion_OneShotDwell(t *testing.T) {
	// Three subtests cover the reduce-motion dwell path and the stale-clear
	// guard introduced in model.go:
	//
	//   falling_edge_schedules_dwell     — rising then falling edge; assert
	//                                      FadeStop==1 and a non-nil dwell cmd.
	//   dwell_clear_resets_in_one_step   — deliver indexBannerClearMsg after
	//                                      the above; assert FadeStop==0, no
	//                                      further cmd (not a fade ladder).
	//   stale_clear_after_reenter_is_noop — falling edge followed by a second
	//                                      rising edge (resets FadeStop to 0),
	//                                      then a stale indexBannerClearMsg
	//                                      must not disturb the in-progress
	//                                      backfill.

	t.Run("falling_edge_schedules_dwell", func(t *testing.T) {
		m := New(Deps{ReduceMotion: true})

		// Rising edge.
		updated, _ := m.Update(IndexProgressMsg{Done: 0, Total: 5, Active: true})
		m = updated.(Model)

		// Falling edge: backfill finishes.
		updated, cmd := m.Update(IndexProgressMsg{Done: 5, Total: 5, Active: false})
		m = updated.(Model)
		if m.indexFadeStop != 1 {
			t.Errorf("after falling edge with ReduceMotion: indexFadeStop = %d, want 1 (full opacity)", m.indexFadeStop)
		}
		if cmd == nil {
			t.Errorf("after falling edge with ReduceMotion: cmd = nil, want one-shot dwell tea.Tick")
		}
	})

	t.Run("dwell_clear_resets_in_one_step", func(t *testing.T) {
		m := New(Deps{ReduceMotion: true})

		// Replay the same rising + falling edge to put the model in dwell state.
		updated, _ := m.Update(IndexProgressMsg{Done: 0, Total: 5, Active: true})
		m = updated.(Model)
		updated, _ = m.Update(IndexProgressMsg{Done: 5, Total: 5, Active: false})
		m = updated.(Model)

		// Simulate the dwell expiring: deliver indexBannerClearMsg directly.
		// One step must clear the banner (FadeStop = 0). Not a fade ladder.
		updated, cmd := m.Update(indexBannerClearMsg{})
		m = updated.(Model)
		if m.indexFadeStop != 0 {
			t.Errorf("after indexBannerClearMsg: indexFadeStop = %d, want 0", m.indexFadeStop)
		}
		if cmd != nil {
			t.Errorf("after indexBannerClearMsg: cmd = %v, want nil (no further ticks)", cmd)
		}
	})

	t.Run("stale_clear_after_reenter_is_noop", func(t *testing.T) {
		m := New(Deps{ReduceMotion: true})

		// Rising edge → falling edge: model enters dwell state (FadeStop=1),
		// dwell tick is conceptually in flight.
		updated, _ := m.Update(IndexProgressMsg{Done: 0, Total: 5, Active: true})
		m = updated.(Model)
		updated, _ = m.Update(IndexProgressMsg{Done: 5, Total: 5, Active: false})
		m = updated.(Model)

		// Second rising edge: a new backfill starts. IndexProgressMsg{Active:true}
		// resets FadeStop to 0, leaving the old dwell tick orphaned in the queue.
		updated, _ = m.Update(IndexProgressMsg{Done: 0, Total: 5, Active: true})
		m = updated.(Model)
		if m.indexFadeStop != 0 {
			t.Errorf("after re-enter active: indexFadeStop = %d, want 0", m.indexFadeStop)
		}

		// The orphaned dwell tick fires. The guard must treat it as stale
		// (FadeStop already 0) and leave the model untouched.
		updated, cmd := m.Update(indexBannerClearMsg{})
		m = updated.(Model)
		if m.indexFadeStop != 0 {
			t.Errorf("after stale clear: indexFadeStop = %d, want 0 (unchanged)", m.indexFadeStop)
		}
		if !m.indexActive {
			t.Errorf("after stale clear: indexActive = false, want true (new backfill should not be disturbed)")
		}
		if cmd != nil {
			t.Errorf("after stale clear: cmd = %v, want nil", cmd)
		}
	})
}

func TestIndexFadeStyle(t *testing.T) {
	// Verifies the three fade stops map to the expected foregrounds.
	// Stop 1 is the default fg (no Foreground set, which lipgloss reports as
	// NoColor{}). Stops 2 and 3 step down through colorMuted and colorFaint —
	// adaptive tokens that render brighter or darker depending on the
	// terminal's background.
	cases := []struct {
		stop int
		want lipgloss.TerminalColor
	}{
		{1, lipgloss.NoColor{}},
		{2, colorMuted},
		{3, colorFaint},
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

func TestViewRendersFadeIndicator(t *testing.T) {
	// Integration check that Model.View() pipes indexFadeStop through
	// to renderIndicators. Without the wiring, the indicator block
	// would render empty even though indexFadeStop is non-zero.
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.indexFadeStop = 2
	m.indexDone = 42

	got := m.View()
	if !strings.Contains(got, "✓ indexed 42") {
		t.Errorf("expected '✓ indexed 42' in View() output; got:\n%s", got)
	}
}

// TestNewModel_DefaultsToCost guards the issue #209 contract: a freshly
// constructed Model must start in cost-mode (m.unitIdx == chartUnitCost)
// before any user input. Resets-on-launch wording lives in model.go:115.
func TestNewModel_DefaultsToCost(t *testing.T) {
	m := New(Deps{})
	if got, want := m.unitIdx, int(chartUnitCost); got != want {
		t.Errorf("New(Deps{}).unitIdx = %d, want %d (chartUnitCost)", got, want)
	}
}

func TestUnitKeyToggles(t *testing.T) {
	// Pressing 'u' cycles unitIdx through 0 (cost) → 1 (output tokens) → 2 (remaining) → 0.
	// Initial state is 0 (cost — default reset per issue #209, no persistence across launches).
	m := New(Deps{})
	m.w, m.h = 120, 40

	if m.unitIdx != 0 {
		t.Fatalf("expected initial unitIdx=0, got %d", m.unitIdx)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.unitIdx != 1 {
		t.Errorf("after first 'u', unitIdx = %d, want 1", m.unitIdx)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.unitIdx != 2 {
		t.Errorf("after second 'u', unitIdx = %d, want 2", m.unitIdx)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.unitIdx != 0 {
		t.Errorf("after third 'u', unitIdx = %d, want 0", m.unitIdx)
	}
}

func TestUnitKeyInHelp(t *testing.T) {
	// The 'u unit' binding must appear in both ShortHelp (footer) and
	// FullHelp (overlay opened with '?'). Asserts on the rendered help
	// strings rather than the KeyMap struct so a misnamed help text
	// surfaces in the test output.
	m := New(Deps{})
	m.w, m.h = 120, 40

	// Footer help line: ShortHelp() result, rendered through bubbles/help.
	// Match the full "u cost/output/usage" pair to avoid false positives —
	// bare "u" also appears in "quit" and "scroll" so the substring is
	// vacuous on its own.
	footer := m.help.View(m.keys)
	if !strings.Contains(footer, "u cost/output/usage") {
		t.Errorf("footer help missing 'u cost/output/usage' binding:\n%s", footer)
	}

	// Help overlay: triggered by '?'. Asserts on the FullHelp view.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	overlay := updated.(Model).View()
	if !strings.Contains(overlay, "cost/output/usage") {
		t.Errorf("help overlay missing 'cost/output/usage' binding:\n%s", overlay)
	}
}

func TestBeginUnitAnimation(t *testing.T) {
	// Drive a model to a known cost state, then call beginUnitAnimation
	// after flipping unitIdx to tokens. Two-phase contract: springActive
	// flips true, springPhase = springShrinking, springProjectiles is
	// sized to the bucket count, springTargetRatios is all zeros (Phase 1
	// target), springFinalTargets holds the new-unit ratios, oldPeak /
	// oldUnitIdx capture the pre-toggle state, and m.peak is the new
	// (tokens) peak so refreshChart already wrote the new viewport content.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Render in cost mode first (default) so lastValues + peak hold the OLD state.
	m.refreshChart()
	if len(m.lastValues) == 0 {
		t.Fatalf("cost-mode lastValues unexpectedly empty")
	}
	oldValues := append([]float64(nil), m.lastValues...)
	oldPeak := m.peak

	// Flip and start animation toward the token values (forward step: cost→tokens).
	m.unitIdx = int(chartUnitTokens)
	m.beginUnitAnimation()

	if !m.springActive {
		t.Errorf("springActive = false, want true")
	}
	if m.springPhase != springShrinking {
		t.Errorf("springPhase = %d, want springShrinking (%d)", m.springPhase, springShrinking)
	}
	if got, want := len(m.springs), len(oldValues); got != want {
		t.Errorf("len(springs) = %d, want %d (one per bucket)", got, want)
	}
	if got, want := len(m.springProjectiles), len(oldValues); got != want {
		t.Errorf("len(springProjectiles) = %d, want %d (one per bucket)", got, want)
	}
	if got, want := len(m.springRatios), len(oldValues); got != want {
		t.Errorf("len(springRatios) = %d, want %d", got, want)
	}
	if got, want := len(m.springVelocities), len(oldValues); got != want {
		t.Errorf("len(springVelocities) = %d, want %d", got, want)
	}
	if got, want := len(m.springTargetRatios), len(oldValues); got != want {
		t.Errorf("len(springTargetRatios) = %d, want %d", got, want)
	}
	if got, want := len(m.springFinalTargets), len(oldValues); got != want {
		t.Errorf("len(springFinalTargets) = %d, want %d", got, want)
	}

	// Initial spring state must equal old cost ratios scaled to niceCeiling(oldPeak).
	// After #250 buildChart uses niceCeilingFloat(peak) as maxValue, so spring
	// ratios use the same denominator to keep bar heights consistent.
	for i, v := range oldValues {
		want := v / niceCeilingFloat(oldPeak)
		if diff := m.springRatios[i] - want; diff < -1e-9 || diff > 1e-9 {
			t.Errorf("springRatios[%d] = %v, want %v (old cost ratio / niceCeiling)", i, m.springRatios[i], want)
		}
	}

	// Phase 1 target is zero across the board.
	for i, r := range m.springTargetRatios {
		if r != 0 {
			t.Errorf("springTargetRatios[%d] = %v, want 0 (Phase 1 target)", i, r)
		}
	}

	// Velocities start at zero (Phase 2 seeds them at the handoff).
	for i, v := range m.springVelocities {
		if v != 0 {
			t.Errorf("springVelocities[%d] = %v, want 0", i, v)
		}
	}

	// springFinalTargets must be the new-unit ratios — in [0, 1] and at
	// least one non-zero.
	var anyNonZero bool
	for i, r := range m.springFinalTargets {
		if r < 0 || r > 1 {
			t.Errorf("springFinalTargets[%d] = %v, want [0, 1]", i, r)
		}
		if r > 0 {
			anyNonZero = true
		}
	}
	if !anyNonZero {
		t.Errorf("all springFinalTargets are zero; expected at least one non-zero token bucket")
	}

	// View() reads oldPeak / oldUnitIdx during Phase 1 to show the OLD
	// unit's label. oldUnitIdx is the unit before m.unitIdx was toggled.
	if m.oldPeak != oldPeak {
		t.Errorf("oldPeak = %v, want %v (pre-toggle cost peak)", m.oldPeak, oldPeak)
	}
	if m.oldUnitIdx != int(chartUnitCost) {
		t.Errorf("oldUnitIdx = %d, want %d (cost, pre-toggle)", m.oldUnitIdx, int(chartUnitCost))
	}

	// m.peak should hold the TOKEN peak — refreshChart already swapped state.
	// Token peaks are integer counts (thousands), so much larger than dollar costs.
	if m.peak == 0 {
		t.Errorf("m.peak unexpectedly 0 after beginUnitAnimation; expected token peak")
	}
	if m.peak <= oldPeak*100 {
		t.Errorf("m.peak = %v should be >> oldPeak = %v; token peak should be much larger than cost for these inputs",
			m.peak, oldPeak)
	}
}

func TestUnitKey_ReduceMotion_SnapsWithoutTick(t *testing.T) {
	// With ReduceMotion enabled, the 'u' keypress must:
	//   - advance unitIdx,
	//   - call refreshChart (so lastValues reflects the new unit),
	//   - leave springActive = false (no animation state),
	//   - return cmd = nil (no springTickMsg follow-up).
	//
	// Seed using the same fixture as TestUnitToggleAnimationSettles, then
	// flip the model's ReduceMotion flag before the keypress.
	m := seedTwoPhaseAnimationModel(t)
	m.deps.ReduceMotion = true
	if len(m.lastValues) == 0 {
		t.Fatalf("seed sanity: lastValues empty after refreshChart in token mode")
	}
	tokensSnapshot := append([]float64(nil), m.lastValues...)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	if cmd != nil {
		t.Errorf("ReduceMotion 'u' press: cmd = %v, want nil (no springTickMsg follow-up)", cmd)
	}
	if m.springActive {
		t.Errorf("ReduceMotion 'u' press: springActive = true, want false (snap, not animate)")
	}
	if m.unitIdx != int(chartUnitTokens) {
		t.Errorf("ReduceMotion 'u' press: unitIdx = %d, want %d (advanced from cost to tokens)",
			m.unitIdx, int(chartUnitTokens))
	}
	// And lastValues must differ from the token snapshot. Bucket counts
	// are identical (same zoom level, same cache state), so a slice-equal
	// check is sufficient — bail with t.Fatalf if lengths somehow diverge.
	if len(m.lastValues) != len(tokensSnapshot) {
		t.Fatalf("ReduceMotion 'u' press: lastValues len = %d, tokensSnapshot len = %d; bucket count changed unexpectedly", len(m.lastValues), len(tokensSnapshot))
	}
	identical := true
	for i := range tokensSnapshot {
		if m.lastValues[i] != tokensSnapshot[i] {
			identical = false
			break
		}
	}
	if identical {
		t.Errorf("ReduceMotion 'u' press: lastValues unchanged from token snapshot; expected cost values after snap")
	}
}

func TestUnitToggleAnimationSettles(t *testing.T) {
	// Drives the model through the two-phase animation: press 'u',
	// then deliver up to 200 springTickMsg ticks. Asserts springActive
	// eventually flips false, ratios converge to targets within
	// epsilon, springPhase ends at springIdle, and the final tick
	// returns no further Cmd (idle = zero cost).
	m := seedTwoPhaseAnimationModel(t)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Fatalf("springActive = false after 'u' press; expected animation start")
	}
	if cmd == nil {
		t.Fatalf("Update returned nil Cmd after 'u' press; expected tea.Tick")
	}

	// 200 ticks (~3.3s @ 60 FPS) is well past the two-phase budget
	// (Phase 1 ≈ 21 ticks, Phase 2 ≈ 30–50 ticks with critical damping
	// and threshold 0.01). Bump maxTicks before reducing the
	// phaseTransitionThreshold below 0.01.
	const maxTicks = 200
	var lastCmd tea.Cmd
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, lastCmd = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("animation did not settle within %d ticks", maxTicks)
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d after settle, want springIdle", m.springPhase)
	}

	// Final ratios must equal targets (snapped on settle).
	for i, r := range m.springRatios {
		if r != m.springTargetRatios[i] {
			t.Errorf("springRatios[%d] = %v, want %v (snapped target)", i, r, m.springTargetRatios[i])
		}
	}

	if lastCmd != nil {
		t.Errorf("settle tick returned non-nil Cmd; idle TUI must not keep ticking")
	}
}

func TestUnitToggle_StaleSpringTickFromPriorAnimation_Dropped(t *testing.T) {
	// Production sequence: press 'u', then press 'u' again BEFORE the first
	// animation's scheduled tick has been delivered. Two springTickMsg are
	// now pending in the bubbletea runtime — one from each press. The first
	// is stale (its generation belongs to the now-aborted animation #1);
	// only the second should advance state.
	//
	// Without generation-aware ticks (issue #218), the stale tick advances
	// animation #2 by one frame, doubling its tick rate. Repeated rapid
	// presses compound the loops until the animation visibly skips.
	m := seedTwoPhaseAnimationModel(t)

	// Seed usage_samples so the second u-press (tokens → remaining line
	// chart) finds non-empty data and beginUnitAnimation arms a real
	// animation #2 — without samples it bails on len(newValues)==0 and
	// springActive flips back to false, hiding the bug we want to test.
	for i := range 5 {
		when := time.Now().UTC().Add(-time.Duration(i) * 30 * time.Minute)
		fiveResets := when.Add(2 * time.Hour)
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: 20.0 + float64(i)*5.0, ResetsAt: &fiveResets},
		}
		if err := m.deps.Cache.RecordUsageSample(u, when); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}

	updated, cmdA := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Fatalf("after first 'u': springActive = false, want true")
	}
	if cmdA == nil {
		t.Fatalf("after first 'u': cmd = nil, want tea.Tick")
	}
	// Resolve cmdA to its springTickMsg payload. tea.Tick blocks for one
	// frame (~16ms @ 60 FPS); acceptable for a one-shot unit test.
	tickA, ok := cmdA().(springTickMsg)
	if !ok {
		t.Fatalf("cmdA produced %T, want springTickMsg", cmdA())
	}

	// Press 'u' again BEFORE delivering tickA. Animation #2 supersedes #1.
	updated, cmdB := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Fatalf("after second 'u': springActive = false, want true")
	}
	if cmdB == nil {
		t.Fatalf("after second 'u': cmd = nil, want tea.Tick")
	}
	tickB, ok := cmdB().(springTickMsg)
	if !ok {
		t.Fatalf("cmdB produced %T, want springTickMsg", cmdB())
	}

	// Snapshot animation #2's freshly-seeded state.
	ratiosBefore := append([]float64(nil), m.springRatios...)
	phaseBefore := m.springPhase

	// Stale tickA must be dropped: no rescheduled Cmd (which would
	// perpetuate the extra tick loop), no spring-state mutation. The
	// dropped-cmd assertion is the sharp behavioral signal — without
	// it, the handler reschedules a fresh tea.Tick, compounding the
	// loop count on every rapid press and accelerating the animation.
	updated, staleCmd := m.Update(tickA)
	m = updated.(Model)
	if staleCmd != nil {
		t.Errorf("stale tick returned non-nil Cmd; must not perpetuate a stale loop")
	}
	for i, r := range m.springRatios {
		if r != ratiosBefore[i] {
			t.Errorf("stale tick advanced springRatios[%d]: before=%v after=%v", i, ratiosBefore[i], r)
		}
	}
	if m.springPhase != phaseBefore {
		t.Errorf("stale tick changed springPhase: before=%d after=%d", phaseBefore, m.springPhase)
	}

	// Live tickB must keep animation #2's frame loop alive — proves the
	// generation gate doesn't accidentally drop fresh ticks too.
	updated, freshCmd := m.Update(tickB)
	m = updated.(Model)
	if freshCmd == nil {
		t.Errorf("live tick returned nil Cmd; animation #2 frame loop must continue")
	}
}

func TestPhaseTransition_AtThreshold(t *testing.T) {
	// Drive ticks until Phase 1 settles. At the handoff:
	//   - springPhase flips to springGrowing.
	//   - springRatios are snapped to 0 (clean visual handoff).
	//   - springTargetRatios takes the values from springFinalTargets.
	//   - springVelocities are seeded as V0 * springFinalTargets[i].
	m := seedTwoPhaseAnimationModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.springPhase != springShrinking {
		t.Fatalf("springPhase = %d after 'u' press, want springShrinking (%d)", m.springPhase, springShrinking)
	}

	// Capture the Phase 2 targets we expect to see after the handoff.
	expectedTargets := append([]float64(nil), m.springFinalTargets...)

	// Sanity probe before the tick loop: capture a non-zero starting ratio
	// so we can verify Phase 1 is actually moving the bars (not silently
	// frozen by a future regression in the Projectile.Update plumbing).
	// A non-trivial input model is required — find a bar that's not at
	// zero and remember its starting ratio.
	probeIdx := -1
	for i, r := range m.springRatios {
		if r > 0 {
			probeIdx = i
			break
		}
	}
	if probeIdx < 0 {
		t.Fatalf("test setup is degenerate — no non-zero springRatios to probe motion")
	}
	probeStart := m.springRatios[probeIdx]

	// Two ticks must move the probe ratio strictly down. Phase 1 starts
	// at zero velocity and harmonica.Projectile uses explicit Euler
	// (position integrates current velocity before acceleration kicks
	// in), so motion appears on tick 2+ — anything ≥ probeStart after
	// two ticks means the Projectile is frozen, almost certainly a
	// range-copy regression in the handler.
	for range 2 {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springRatios[probeIdx] >= probeStart {
		t.Errorf("after two ticks, springRatios[%d] = %v >= %v (start) — Phase 1 not moving",
			probeIdx, m.springRatios[probeIdx], probeStart)
	}

	// Drive ticks while still in Phase 1.
	const maxTicks = 100
	for i := 0; i < maxTicks && m.springPhase == springShrinking; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springPhase == springShrinking {
		t.Fatalf("Phase 1 did not exit within %d ticks", maxTicks)
	}
	// Phase 1 → Hold handoff: ratios are zero but Phase 2 is NOT seeded
	// yet (that work moves to the springHolding case, fired by the hold
	// tick below) — see #163 and TestUnitToggleAnimation_HoldPhaseTransitions
	// for the dedicated hold-phase coverage.
	if m.springPhase != springHolding {
		t.Fatalf("springPhase = %d after Phase 1 exit, want springHolding (%d)", m.springPhase, springHolding)
	}
	for i, r := range m.springRatios {
		if r != 0 {
			t.Errorf("springRatios[%d] = %v after Phase 1 handoff, want 0", i, r)
		}
	}

	// Deliver the hold tick to transition Hold → Phase 2 and seed it.
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)

	if m.springPhase != springGrowing {
		t.Fatalf("springPhase = %d after hold tick, want springGrowing (%d)", m.springPhase, springGrowing)
	}
	// springTargetRatios must now hold the Phase 2 destination.
	for i, want := range expectedTargets {
		if m.springTargetRatios[i] != want {
			t.Errorf("springTargetRatios[%d] = %v after hold tick, want %v",
				i, m.springTargetRatios[i], want)
		}
	}
	// springVelocities must be seeded as V0 * springFinalTargets[i].
	for i, want := range expectedTargets {
		got := m.springVelocities[i]
		exp := phase2InitialVelocityV0 * want
		if diff := got - exp; diff < -1e-9 || diff > 1e-9 {
			t.Errorf("springVelocities[%d] = %v, want %v (V0 * springFinalTargets[%d])", i, got, exp, i)
		}
	}
}

func TestUnitToggleAnimation_HoldPhaseTransitions(t *testing.T) {
	// Verifies the springHolding phase between Phase 1 (fall) and Phase 2
	// (grow). After Phase 1 hits threshold:
	//   - springPhase = springHolding (NOT springGrowing).
	//   - springRatios snapped to 0.
	//   - Phase 2 state (springTargetRatios, springVelocities) NOT yet
	//     seeded — that work moves to the springHolding handler.
	// One more tick (the hold tick) transitions to springGrowing and
	// seeds Phase 2.
	m := seedTwoPhaseAnimationModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.springPhase != springShrinking {
		t.Fatalf("springPhase = %d after 'u' press, want springShrinking (%d)", m.springPhase, springShrinking)
	}

	// Capture the Phase 2 targets we expect after the hold tick.
	expectedTargets := append([]float64(nil), m.springFinalTargets...)

	// Drive ticks while still in Phase 1.
	const maxTicks = 100
	for i := 0; i < maxTicks && m.springPhase == springShrinking; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springPhase == springShrinking {
		t.Fatalf("Phase 1 did not exit within %d ticks", maxTicks)
	}

	// After Phase 1 exit: must be in springHolding, NOT springGrowing.
	if m.springPhase != springHolding {
		t.Fatalf("springPhase = %d after Phase 1 exit, want springHolding (%d)", m.springPhase, springHolding)
	}

	// Ratios snapped to zero.
	for i, r := range m.springRatios {
		if r != 0 {
			t.Errorf("springRatios[%d] = %v during springHolding, want 0", i, r)
		}
	}

	// Phase 2 state NOT yet seeded.
	for i, v := range m.springVelocities {
		if v != 0 {
			t.Errorf("springVelocities[%d] = %v during springHolding, want 0 (Phase 2 not seeded yet)", i, v)
		}
	}
	for i, tr := range m.springTargetRatios {
		if tr != 0 {
			t.Errorf("springTargetRatios[%d] = %v during springHolding, want 0 (Phase 2 not seeded yet)", i, tr)
		}
	}

	// Deliver the hold tick. This is the springTickMsg the one-shot
	// tea.Tick(phaseHoldDuration) would have produced.
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)

	// Now in springGrowing.
	if m.springPhase != springGrowing {
		t.Fatalf("springPhase = %d after hold tick, want springGrowing (%d)", m.springPhase, springGrowing)
	}

	// Phase 2 targets and velocities seeded.
	for i, want := range expectedTargets {
		if m.springTargetRatios[i] != want {
			t.Errorf("springTargetRatios[%d] = %v after hold tick, want %v",
				i, m.springTargetRatios[i], want)
		}
		exp := phase2InitialVelocityV0 * want
		got := m.springVelocities[i]
		if diff := got - exp; diff < -1e-9 || diff > 1e-9 {
			t.Errorf("springVelocities[%d] = %v after hold tick, want %v (V0 * springFinalTargets[%d])",
				i, got, exp, i)
		}
	}
}

func TestPhase2Settle_ClearsState(t *testing.T) {
	// Drive ticks all the way through both phases. After settle:
	//   - springActive = false.
	//   - springPhase = springIdle.
	//   - springRatios are snapped to springTargetRatios.
	//   - last tick returns no further Cmd.
	m := seedTwoPhaseAnimationModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	// 200 ticks covers both phases comfortably (Phase 1 ≈ 21 ticks @
	// 60 FPS, Phase 2 ≈ 30–50 ticks with critical damping).
	const maxTicks = 200
	var lastCmd tea.Cmd
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, lastCmd = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("animation did not settle within %d ticks", maxTicks)
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d after settle, want springIdle (%d)", m.springPhase, springIdle)
	}
	if lastCmd != nil {
		t.Errorf("settle tick returned non-nil Cmd; idle TUI must not keep ticking")
	}
}

func TestRefreshDuringAnimationSnapsAndContinues(t *testing.T) {
	// While animation is active, a RefreshMsg whose data has a different
	// bucket count must abort the spring (snap to targets) and re-run
	// refreshChart against the new data. The model after the RefreshMsg
	// must NOT be springActive and must have lastValues reflecting the
	// new bucket count.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	// Initial state: one message.
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-2 * time.Hour), InputTokens: 10000, OutputTokens: 5000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()
	// Post-open state — intro already fired (see seedTwoPhaseAnimationModel).
	m.introPending = false
	preBucketCount := len(m.lastValues)

	// Start animation.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Fatalf("animation didn't start")
	}

	// Insert a new message far back in time so earliest-bucket moves and the
	// bucket count grows substantially.
	newMsgs := []parse.Message{
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-12 * time.Hour), InputTokens: 50000, OutputTokens: 25000},
	}
	if err := c.InsertMessages(newMsgs, tab); err != nil {
		t.Fatalf("InsertMessages (new): %v", err)
	}

	// Deliver RefreshMsg mid-animation.
	updated, _ = m.Update(RefreshMsg{})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("springActive = true after RefreshMsg; expected snap-and-stop")
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d after RefreshMsg; expected springIdle", m.springPhase)
	}
	if len(m.lastValues) <= preBucketCount {
		t.Errorf("lastValues not refreshed after RefreshMsg; got %d buckets, want > %d",
			len(m.lastValues), preBucketCount)
	}
}

func TestRefreshDoesNotAnimate(t *testing.T) {
	// RefreshMsg without a prior 'u' press must NOT start an animation.
	// Guards against the watcher loop accidentally springing the chart
	// every time a new message arrives.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: time.Now().UTC().Add(-time.Hour), InputTokens: 1000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	// Post-open state — intro already fired (see seedTwoPhaseAnimationModel).
	m.introPending = false

	updated, _ := m.Update(RefreshMsg{})
	m = updated.(Model)
	if m.springActive {
		t.Errorf("springActive = true after RefreshMsg; watcher refresh must not animate")
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d after RefreshMsg without prior 'u'; want springIdle", m.springPhase)
	}
	if len(m.springs) != 0 {
		t.Errorf("springs slice non-empty after RefreshMsg; expected len=0")
	}
}

func TestRefreshChart_CostMode(t *testing.T) {
	// With unitIdx=int(chartUnitCost), refreshChart must call CostBuckets and cache
	// the cost values into m.lastValues, with m.peak set to max(cost).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Token mode (explicit — default is now cost per issue #209): peak should
	// match max output_tokens in any bucket.
	m.unitIdx = int(chartUnitTokens)
	m.refreshChart()
	tokenPeak := m.peak
	if tokenPeak == 0 {
		t.Fatalf("token-mode peak unexpectedly 0")
	}
	if len(m.lastValues) == 0 {
		t.Fatalf("token-mode lastValues unexpectedly empty")
	}

	// Cost mode: same buckets, but peak/lastValues now reflect dollar cost.
	m.unitIdx = int(chartUnitCost)
	m.refreshChart()
	costPeak := m.peak
	if costPeak == 0 {
		t.Fatalf("cost-mode peak unexpectedly 0")
	}
	if len(m.lastValues) == 0 {
		t.Fatalf("cost-mode lastValues unexpectedly empty")
	}

	// Cost magnitudes are dollars; tokens are integer counts. They MUST
	// differ by orders of magnitude for these test inputs (10k–30k input
	// tokens at Opus rates produces sub-dollar costs). If they're close,
	// either CostBuckets is returning token totals or lastValues isn't
	// being routed through the cost branch.
	if costPeak >= tokenPeak/100 {
		t.Errorf("cost peak (%v) suspiciously close to token peak (%v); cost branch may not be wired",
			costPeak, tokenPeak)
	}
}

func TestScrollHelpers_UpdateShadowOffset(t *testing.T) {
	// Direct unit test of setX / scrollLeft / scrollRight: clamp behaviour
	// and shadow synchronisation against a Model with a known lastStarts
	// length and chartWidth.
	t.Parallel()

	newModel := func() *Model {
		m := New(Deps{})
		m.w, m.h = 120, 40
		m.viewport.Width = m.chartWidth()
		m.viewport.Height = m.chartHeight()
		// Seed lastStarts so setX has a non-zero maxX to clamp against.
		// chartWidth at w=120 is 118 (see TestChartWidth_FloorsAtTen); pick
		// lastStarts length 200 → maxX = 200 - 118 = 82.
		m.lastStarts = make([]time.Time, 200)
		// SetContent so the viewport's own clamp also has content to work
		// against (longestLineWidth >= 200).
		m.viewport.SetContent(strings.Repeat("X", 200))
		return &m
	}

	tests := []struct {
		name       string
		setup      func(*Model)
		op         func(*Model)
		wantShadow int
	}{
		{"setX 0 → 0", func(m *Model) {}, func(m *Model) { m.setX(0) }, 0},
		{"setX past max clamps to maxX", func(m *Model) {}, func(m *Model) { m.setX(500) }, 82},
		{"setX negative clamps to 0", func(m *Model) {}, func(m *Model) { m.setX(-5) }, 0},
		{"scrollLeft from 50 by 10 → 40", func(m *Model) { m.setX(50) }, func(m *Model) { m.scrollLeft(10) }, 40},
		{"scrollRight from 75 by 10 → 82 (clamped)", func(m *Model) { m.setX(75) }, func(m *Model) { m.scrollRight(10) }, 82},
		{"scrollLeft below 0 clamps to 0", func(m *Model) { m.setX(5) }, func(m *Model) { m.scrollLeft(10) }, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newModel()
			tt.setup(m)
			tt.op(m)
			if m.viewportXOffset != tt.wantShadow {
				t.Errorf("viewportXOffset = %d, want %d", m.viewportXOffset, tt.wantShadow)
			}
		})
	}
}

// seedScrollTestModel builds a Model + Cache pair seeded with `count`
// messages spaced 15 minutes apart ending now. With count=500 the cache
// spans ~125h, which at 15m zoom (zoomIdx=0) produces ~500 buckets vs
// chartWidth=118 at w=120 — plenty of scroll room. At 1h zoom the same
// cache produces ~125 buckets, still overflowing chartWidth=118, so the
// zoom-translation subtest also has somewhere to scroll.
//
// Also seeds 10 usage_samples (5min apart, ending now) so remaining-mode
// refreshes hit the non-empty pts5h/pts7d branch. Bar-mode tests are
// unaffected (lastPts5h/7d only consulted in remaining mode).
//
// Returns the Model with chartWidth=118 (w=120) and zoom=15m (zoomIdx=0).
// Caller is responsible for cache.Close via the returned cleanup.
func seedScrollTestModel(t *testing.T, count int) (*Model, func()) {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	tab, err := pricing.Load()
	if err != nil {
		c.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := make([]parse.Message, count)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: int64(1000 + i*10),
		}
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		c.Close()
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: float64(10 + i*5), ResetsAt: timePtr(now.Add(time.Hour))},
			SevenDay: &anthro.Bucket{Utilization: float64(5 + i*2), ResetsAt: timePtr(now.Add(24 * time.Hour))},
		}
		if err := c.RecordUsageSample(u, now.Add(time.Duration(-i)*5*time.Minute)); err != nil {
			c.Close()
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.zoomIdx = 0 // 15m zoom: count messages → ~count buckets, overflows chartWidth=118
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()
	return &m, func() { c.Close() }
}

func TestZoomKey_EnabledInEveryUnitMode(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 200)
	defer cleanup()

	for _, u := range []chartUnit{chartUnitTokens, chartUnitCost, chartUnitRemaining} {
		m.unitIdx = int(u)
		if !m.keys.Zoom.Enabled() {
			t.Errorf("Zoom binding disabled in unit=%v; want enabled", u)
		}
	}
}

func TestZoomKey_AdvancesZoomInRemainingMode(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 200)
	defer cleanup()
	m.unitIdx = int(chartUnitRemaining)
	m.zoomIdx = 0
	m.refreshChart()

	zoomKey := m.keys.Zoom.Keys()[0]
	mUpdated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(zoomKey)})
	got := mUpdated.(Model).zoomIdx
	if got != 1 {
		t.Errorf("zoomIdx after %q in remaining mode = %d; want 1", zoomKey, got)
	}
}

// TestRemainingMode_CanvasWidthMatchesBar verifies the new canvas
// width formula matches what bar mode would produce at the same zoom
// for the same [from, to) window. This is the time-range parity the
// u-toggle anchor preservation relies on.
func TestRemainingMode_CanvasWidthMatchesBar(t *testing.T) {
	for zoomIdx, zoom := range ZoomLevels {
		t.Run(zoom.Label, func(t *testing.T) {
			m, cleanup := seedScrollTestModel(t, 200)
			defer cleanup()
			m.zoomIdx = zoomIdx

			m.unitIdx = int(chartUnitTokens)
			m.refreshChart()
			barCanvasW := m.lastCanvasW
			barFrom, barTo := m.lastChartFrom, m.lastChartTo

			m.unitIdx = int(chartUnitRemaining)
			m.refreshChart()
			remCanvasW := m.lastCanvasW
			remFrom, remTo := m.lastChartFrom, m.lastChartTo

			if !barFrom.Equal(remFrom) || !barTo.Equal(remTo) {
				t.Errorf("[%s] from/to differ: bar=[%v,%v) rem=[%v,%v)",
					zoom.Label, barFrom, barTo, remFrom, remTo)
			}
			want := barCanvasW
			if want < m.chartWidth() {
				want = m.chartWidth()
			}
			if remCanvasW != want {
				t.Errorf("[%s] canvasW differ: bar=%d rem=%d (chartWidth=%d, want=%d)",
					zoom.Label, barCanvasW, remCanvasW, m.chartWidth(), want)
			}
		})
	}
}

// TestRefreshChart_PreservesAnchorAcrossRefresh verifies the wall-clock
// anchor stays put across a no-op refresh (same zoom, same unit, no new
// data). Locks in the new time-based anchor primitive — a regression
// here means a viewport jump on every watcher RefreshMsg.
func TestRefreshChart_PreservesAnchorAcrossRefresh(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 200)
	defer cleanup()
	m.unitIdx = int(chartUnitTokens)
	m.refreshChart()

	// Scroll to a non-edge position (column-equivalent at stride=1 zoom).
	canvasW := m.lastCanvasW
	if canvasW < 20 {
		t.Skipf("seeded canvas too narrow (%d) to scroll", canvasW)
	}
	want := canvasW / 3
	m.setX(want)

	// No-op refresh — same data, same zoom, same unit.
	m.refreshChart()

	if got := m.viewportXOffset; absInt(got-want) > 1 {
		t.Errorf("after no-op refresh viewportXOffset = %d; want within +-1 of %d", got, want)
	}
}

// TestRefreshChart_PinnedSticksToRightEdge verifies the wasPinned snap
// survives the migration to time-based anchoring.
func TestRefreshChart_PinnedSticksToRightEdge(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 200)
	defer cleanup()
	m.unitIdx = int(chartUnitTokens)
	m.refreshChart()

	// Pin to right edge (column-equivalent at stride=1 zoom).
	rightEdge := max(0, m.lastCanvasW-m.viewport.Width)
	m.setX(rightEdge)

	m.refreshChart()

	if got := m.viewportXOffset; got != max(0, m.lastCanvasW-m.viewport.Width) {
		t.Errorf("pinned refresh viewportXOffset = %d; want right edge %d",
			got, max(0, m.lastCanvasW-m.viewport.Width))
	}
}

func TestRefreshChart_CapturesCanvasState(t *testing.T) {
	tests := []struct {
		name    string
		unitIdx int
	}{
		{"tokens", int(chartUnitTokens)},
		{"cost", int(chartUnitCost)},
		{"remaining", int(chartUnitRemaining)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cleanup := seedScrollTestModel(t, 200)
			defer cleanup()
			m.unitIdx = tt.unitIdx

			m.refreshChart()

			if m.lastCanvasW <= 0 {
				t.Errorf("lastCanvasW = %d after refresh in %s mode; want > 0", m.lastCanvasW, tt.name)
			}
			if m.lastChartFrom.IsZero() {
				t.Errorf("lastChartFrom is zero after refresh in %s mode", tt.name)
			}
			if m.lastChartTo.IsZero() {
				t.Errorf("lastChartTo is zero after refresh in %s mode", tt.name)
			}
			if !m.lastChartTo.After(m.lastChartFrom) {
				t.Errorf("lastChartTo (%v) is not after lastChartFrom (%v) in %s mode",
					m.lastChartTo, m.lastChartFrom, tt.name)
			}
		})
	}
}

func TestRefreshChart_FirstLoadPinsRight(t *testing.T) {
	// First refresh (no prior lastStarts → !hadAnchor branch) should
	// pin the viewport to the right edge: viewportXOffset == maxX.
	m, cleanup := seedScrollTestModel(t, 500)
	defer cleanup()

	maxX := max(0, len(m.lastStarts)-m.chartWidth())
	if m.viewportXOffset != maxX {
		t.Errorf("viewportXOffset = %d, want %d (right edge)", m.viewportXOffset, maxX)
	}
}

func TestRefreshChart_PreservesWallClockAnchor(t *testing.T) {
	// Verifies the wall-clock anchor survives every refresh trigger
	// type. wantPinned is true when the trigger should leave the user
	// pinned to the right edge (only when the user was already pinned
	// before the trigger fired). Otherwise the leftmost visible bucket
	// after the trigger must equal the bucket that was leftmost before,
	// modulo BucketAlign to the active zoom (only the zoom case crosses
	// densities).
	//
	// Scroll amount and count are chosen so the anchor index remains
	// within maxX after every trigger:
	//   - count=700, zoomIdx=0 (15m): ~700 buckets, maxX≈582 at w=120
	//   - scrollLeft(450): offset≈132, safely mid-chart
	//   - after zoom to 1h: ~175 buckets, maxX≈57; BucketAlign(anchor,1h)
	//     lands near now−(568×15m)=~142h ago, well within the 1h grid
	//   - after resize w=160: chartWidth=158, maxX≈542; offset 132 < 542
	tests := []struct {
		name        string
		startPinned bool
		trigger     func(m *Model)
	}{
		{"refresh keeps scrolled anchor", false, func(m *Model) { m.refreshChart() }},
		{"refresh keeps pinned user pinned", true, func(m *Model) { m.refreshChart() }},
		{"zoom translates anchor to new density", false, func(m *Model) {
			m.zoomIdx = (m.zoomIdx + 1) % len(ZoomLevels)
			m.refreshChart()
		}},
		{"unit toggle keeps anchor", false, func(m *Model) {
			m.unitIdx = (m.unitIdx + 1) % int(chartUnitCount)
			m.refreshChart()
		}},
		{"resize keeps anchor", false, func(m *Model) {
			m.w = 160
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			m.refreshChart()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cleanup := seedScrollTestModel(t, 700)
			defer cleanup()

			// Set up scroll precondition.
			var anchorTime time.Time
			if tt.startPinned {
				// Already pinned by seedScrollTestModel's initial refreshChart.
				if m.viewportXOffset != max(0, len(m.lastStarts)-m.chartWidth()) {
					t.Fatalf("setup: expected pinned-right, got viewportXOffset=%d", m.viewportXOffset)
				}
			} else {
				// Scroll left to a mid-chart position. Use 450 steps so the
				// anchor (≈132 buckets from left at 15m = ~142h ago) remains
				// within maxX at every post-trigger zoom/size — including after
				// zoom to 1h (maxX≈57, anchor at pos≈33) and resize to w=160
				// (maxX≈542).
				m.scrollLeft(450)
				if m.viewportXOffset == 0 || m.viewportXOffset == max(0, len(m.lastStarts)-m.chartWidth()) {
					t.Fatalf("setup: scroll should land mid-chart, got viewportXOffset=%d", m.viewportXOffset)
				}
				anchorTime = m.lastStarts[m.viewportXOffset]
			}

			tt.trigger(m)

			if tt.startPinned {
				maxX := max(0, len(m.lastStarts)-m.chartWidth())
				if m.viewportXOffset != maxX {
					t.Errorf("after trigger: viewportXOffset = %d, want %d (still pinned)", m.viewportXOffset, maxX)
				}
				return
			}

			zoom := ZoomLevels[m.zoomIdx]
			wantAnchor := cache.BucketAlign(anchorTime, zoom.Duration)
			got := m.lastStarts[m.viewportXOffset]
			if !got.Equal(wantAnchor) {
				t.Errorf("after trigger: anchor at viewport left = %v, want %v (BucketAlign of pre-trigger anchor %v to %s)",
					got, wantAnchor, anchorTime, zoom.Label)
			}
		})
	}
}

// TestRefreshChart_PreservesScroll_Issue134 is a regression-named guard
// for https://github.com/martinciu/ccpulse/issues/134 — the bug where
// a watcher-driven RefreshMsg re-pinned the user back to "now" every
// ~100ms during an active Claude session. Same shape as the table-
// driven "refresh keeps scrolled anchor" case but kept separate so a
// bisect on the issue number lands on a focused failure.
func TestRefreshChart_PreservesScroll_Issue134(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 500)
	defer cleanup()

	m.scrollLeft(30)
	anchorTime := m.lastStarts[m.viewportXOffset]
	// Windowing (#255) makes viewport.HorizontalScrollPercent degenerate —
	// the content is now ~viewport width, so it sits at ~1.0 regardless of
	// position. The shadow viewportXOffset is the position-in-full-history
	// metric; assert the scroll moved off the right edge there instead.
	rightEdge := len(m.lastStarts) - m.visibleBuckets()
	if m.viewportXOffset >= rightEdge {
		t.Fatalf("setup: scroll should not be at right edge, got viewportXOffset=%d (rightEdge=%d)",
			m.viewportXOffset, rightEdge)
	}

	m.refreshChart()

	got := m.lastStarts[m.viewportXOffset]
	if !got.Equal(anchorTime) {
		t.Errorf("anchor drifted across refresh: %v → %v", anchorTime, got)
	}
}

// TestRefreshChart_ScrollAnchorAcrossZooms verifies the 24h zoom path:
// - ioTokenBucketsDaily returns day-aligned buckets (~24h apart)
// - the anchor is preserved across a subsequent refreshChart in 24h zoom
//
// Seeds 40 days of data (40*24*4=3840 15m-spaced messages) so that 24h
// zoom produces ~40 buckets with scroll room beyond visibleBuckets_24h=23.
func TestRefreshChart_ScrollAnchorAcrossZooms(t *testing.T) {
	const days = 40
	m, cleanup := seedScrollTestModel(t, days*24*4)
	defer cleanup()

	// Switch to 24h zoom and rebuild.
	m.zoomIdx = 2
	m.refreshChart()

	if len(m.lastStarts) == 0 {
		t.Fatal("24h zoom: lastStarts empty")
	}

	// Each bucket must start exactly one local calendar day after the
	// previous (23h≤gap≤25h covers DST spring/fall transitions).
	for i := 1; i < len(m.lastStarts); i++ {
		gap := m.lastStarts[i].Sub(m.lastStarts[i-1])
		if gap < 23*time.Hour || gap > 25*time.Hour {
			t.Errorf("bucket[%d] gap = %v, want 23h–25h (local day)", i, gap)
		}
	}

	// Scroll to a mid-chart position and verify anchor persists across refresh.
	nv := m.visibleBuckets()
	if len(m.lastStarts) <= nv {
		t.Skipf("not enough 24h buckets for scroll room: %d ≤ visibleBuckets %d", len(m.lastStarts), nv)
	}
	m.scrollLeft(5)
	if m.viewportXOffset == 0 {
		t.Fatal("scrollLeft(5) did not move; no scroll room")
	}
	anchorTime := m.lastStarts[m.viewportXOffset]

	m.refreshChart()

	got := m.lastStarts[m.viewportXOffset]
	if !got.Equal(anchorTime) {
		t.Errorf("anchor drifted at 24h zoom across refresh: %v → %v", anchorTime, got)
	}
}

func TestView_CostModeRendersDollarPrefix(t *testing.T) {
	// End-to-end check that flipping unitIdx to cost causes the rendered
	// View() to contain a "$"-prefixed Y label. Guards the path
	// unitIdx -> chartUnit(m.unitIdx) -> overlayYLabel -> formatUnitValue;
	// a casting bug or wrong constant routing would silently strip the
	// prefix and no unit-level test would catch it.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Token mode (explicit — default is cost per issue #209): View() must NOT contain a "$" Y label.
	m.unitIdx = int(chartUnitTokens)
	m.refreshChart()
	if strings.Contains(m.View(), "$") {
		t.Errorf("token-mode View contains '$' unexpectedly:\n%s", m.View())
	}

	// Cost mode: View() must contain a "$" Y label.
	m.unitIdx = int(chartUnitCost)
	m.refreshChart()
	if !strings.Contains(m.View(), "$") {
		t.Errorf("cost-mode View missing '$' Y label:\n%s", m.View())
	}
}

func TestQuotaMsg_DoesNotRebuildChart(t *testing.T) {
	// QuotaMsg should update the header (m.quota/recomputeWindow) but
	// NOT rebuild the chart. Verify by: scroll to a mid-chart anchor,
	// snapshot the rendered chart bytes, send a QuotaMsg, assert chart
	// bytes are unchanged AND viewportXOffset is unchanged.
	m, cleanup := seedScrollTestModel(t, 300)
	defer cleanup()

	m.scrollLeft(30)
	beforeOffset := m.viewportXOffset
	beforeView := m.viewport.View()

	updated, _ := m.Update(QuotaMsg{
		Usage:     nil,
		Source:    "cache_fresh",
		UpdatedAt: time.Now(),
	})
	mm := updated.(Model)

	if mm.viewportXOffset != beforeOffset {
		t.Errorf("viewportXOffset changed across QuotaMsg: %d → %d", beforeOffset, mm.viewportXOffset)
	}
	if mm.viewport.View() != beforeView {
		t.Errorf("viewport rendered output changed across QuotaMsg (chart should not rebuild)")
	}
}

func TestUnitToggle_SpringStartsAtScrolledOffset(t *testing.T) {
	// A scrolled-away user pressing 'u' to toggle cost↔tokens should
	// have the spring animation start from their actual viewport offset,
	// not from the right edge. Otherwise the animation renders against
	// the wrong slice of bars.
	m, cleanup := seedScrollTestModel(t, 300)
	defer cleanup()

	m.scrollLeft(30)
	scrolledOffset := m.viewportXOffset
	rightEdge := max(0, len(m.lastValues)-m.chartWidth())
	if scrolledOffset == rightEdge {
		t.Fatalf("setup: scroll should land away from right edge, got %d == %d", scrolledOffset, rightEdge)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	mm := updated.(Model)

	if mm.springXOffset != scrolledOffset {
		t.Errorf("springXOffset = %d, want %d (the user's actual viewport offset)", mm.springXOffset, scrolledOffset)
	}
}

// TestUnitToggle_24hPinnedScrollRestoration verifies that pressing 'u' at the
// right edge of a 24h-zoom chart always restores viewportXOffset to maxX, not
// maxX-1. At 24h zoom stride=12; when (canvasW - vpWidth) is not divisible by
// 12 the old floor-division produced maxX-1 at certain terminal widths.
func TestUnitToggle_24hPinnedScrollRestoration(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}

	// Seed 20 days of data (one message per day) so maxX > 0 at all tested widths.
	now := time.Now()
	today := cache.DayStartLocal(now)
	msgs := make([]parse.Message, 20)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   today.AddDate(0, 0, -(19 - i)),
			InputTokens: int64(1000 + i*100),
		}
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	cases := []struct {
		w    int
		name string
		bug  bool // true = was broken before the fix
	}{
		{100, "w=100", true},
		{121, "w=121", true},
		{120, "w=120", false},
		{132, "w=132", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Deps{Cache: c})
			m.w, m.h = tc.w, 40
			m.zoomIdx = 2 // 24h zoom
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			m.introPending = false
			m.refreshChart()

			if len(m.lastStarts) == 0 {
				t.Fatal("no 24h buckets after seed")
			}

			maxX := max(0, len(m.lastStarts)-m.visibleBuckets())
			if maxX == 0 {
				t.Skipf("w=%d produces no scroll room (maxX=0)", tc.w)
			}

			// Scroll to right edge.
			m.setX(maxX)
			if m.viewportXOffset != maxX {
				t.Fatalf("setup: setX(%d) -> viewportXOffset=%d", maxX, m.viewportXOffset)
			}

			// Press 'u' to toggle unit.
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
			mm := updated.(Model)

			if mm.viewportXOffset != maxX {
				t.Errorf("viewportXOffset = %d, want %d (maxX); delta = %d (was %v before fix)",
					mm.viewportXOffset, maxX, mm.viewportXOffset-maxX, tc.bug)
			}
			if mm.springXOffset != maxX {
				t.Errorf("springXOffset = %d, want %d (maxX)", mm.springXOffset, maxX)
			}

			// At the first width, verify the first Phase 1 frame has bars visible
			// (today's bucket is still rendered, even mid-shrink).
			if tc.w == 100 {
				view := mm.View()
				body := strings.Join(chartBodyLines(view), "\n")
				if !strings.ContainsAny(body, "▁▂▃▄▅▆▇█") {
					t.Errorf("Phase 1 frame-0 chart body has no bar glyphs; today's bucket dropped:\n%s", body)
				}
			}

			// Drive through Phase 1 → Hold → Phase 2 → settle.
			m = mm
			const maxTicks = 200
			for i := 0; i < maxTicks && m.springActive; i++ {
				var u tea.Model
				u, _ = m.Update(springTickMsg{gen: m.springGen})
				m = u.(Model)
			}
			if m.springActive {
				t.Fatalf("animation never settled in %d ticks", maxTicks)
			}

			if m.viewportXOffset != maxX {
				t.Errorf("post-settle viewportXOffset = %d, want %d (maxX)", m.viewportXOffset, maxX)
			}
		})
	}
}

// TestUnitToggle_24hCycle verifies that a full 'u' cycle
// (cost → tokens → remaining → cost) preserves the user's right-edge
// anchor at every step. The first leg of #206 fixed bar→bar transitions;
// this test locks the bar↔line legs, where the original patch routed
// remaining mode through a still-buggy setX(rightEdgeCol/stride). Once
// the second toggle (tokens→remaining) snapped the offset to maxX−1, every
// subsequent toggle inherited the broken position via refreshChart's
// anchor-time round-trip and the chart sat one stride left of the right
// edge with today's bucket clipped or absent from the spring frame.
//
// Seeds usage_samples in addition to messages so cost→remaining and
// remaining→tokens both fire the two-phase spring (not the empty-data
// early return in beginUnitAnimation).
func TestUnitToggle_24hCycle(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}

	now := time.Now()
	today := cache.DayStartLocal(now)
	msgs := make([]parse.Message, 20)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   today.AddDate(0, 0, -(19 - i)),
			InputTokens: int64(1000 + i*100),
		}
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	for i := 0; i < 5; i++ {
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: float64(10 + i*5), ResetsAt: timePtr(now.Add(time.Hour))},
			SevenDay: &anthro.Bucket{Utilization: float64(5 + i*2), ResetsAt: timePtr(now.Add(24 * time.Hour))},
		}
		if err := c.RecordUsageSample(u, now.Add(time.Duration(-i)*time.Hour)); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}

	cases := []struct {
		w    int
		name string
	}{
		{100, "w=100"}, // bug width
		{121, "w=121"}, // bug width
		{120, "w=120"}, // stride-aligned (was already passing)
		{132, "w=132"}, // stride-aligned (was already passing)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Deps{Cache: c})
			m.w, m.h = tc.w, 40
			m.zoomIdx = 2 // 24h
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			m.introPending = false
			m.refreshChart()

			maxX := max(0, len(m.lastStarts)-m.visibleBuckets())
			if maxX == 0 {
				t.Skipf("w=%d produces no scroll room (maxX=0)", tc.w)
			}
			m.setX(maxX)
			if m.viewportXOffset != maxX {
				t.Fatalf("setup: setX(%d) -> viewportXOffset=%d", maxX, m.viewportXOffset)
			}

			// Cycle through all three transitions; assert the right-edge
			// anchor is preserved at every step. In production
			// viewport.Width == chartWidth(), so bar-mode and
			// remaining-mode maxX coincide; one maxX suffices for all
			// three modes at the same zoom.
			transitions := []string{"cost→tokens", "tokens→remaining", "remaining→cost"}
			for i, label := range transitions {
				updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
				mm := updated.(Model)

				if mm.springActive && mm.springXOffset != maxX {
					t.Errorf("toggle %d (%s): springXOffset = %d, want %d (today's bucket would be sliced off the spring frame)",
						i+1, label, mm.springXOffset, maxX)
				}

				const maxTicks = 200
				for j := 0; j < maxTicks && mm.springActive; j++ {
					u, _ := mm.Update(springTickMsg{gen: mm.springGen})
					mm = u.(Model)
				}
				if mm.springActive {
					t.Fatalf("toggle %d (%s): animation never settled in %d ticks", i+1, label, maxTicks)
				}
				if mm.viewportXOffset != maxX {
					t.Errorf("toggle %d (%s): post-settle viewportXOffset = %d, want %d",
						i+1, label, mm.viewportXOffset, maxX)
				}
				m = mm
			}
		})
	}
}

func TestYLabel_Phase1ShowsOldUnit(t *testing.T) {
	// During Phase 1 (shrinking), View() must render the Y-label with
	// the OLD unit's value and the OLD peak. We assert by string
	// inspection: the rendered View must contain a cost-format label
	// (e.g. "$0.50") since the default is now cost per issue #209.
	m := seedTwoPhaseAnimationModel(t)

	// We're starting from cost (default); toggle to tokens. Phase 1 should
	// show the old (cost) label with '$'.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if m.springPhase != springShrinking {
		t.Fatalf("springPhase = %d, want springShrinking", m.springPhase)
	}

	// Drive a handful of ticks but stay inside Phase 1.
	for i := 0; i < 5 && m.springPhase == springShrinking; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springPhase != springShrinking {
		t.Fatalf("expected to stay in Phase 1 for 5 ticks; got phase %d", m.springPhase)
	}

	body := strings.Join(chartBodyLines(m.View()), "\n")
	// Phase 1 should still expose a cost-shaped label (contains '$')
	// — no token-only format in the chart body.
	if !strings.Contains(body, "$") {
		t.Errorf("Phase 1 chart body should show OLD (cost) Y-label with '$'; got body:\n%s", body)
	}
}

func TestYLabel_Phase2ShowsNewUnit(t *testing.T) {
	// During Phase 2 (growing), View() must render the Y-label with
	// the NEW unit's value and the NEW peak. After toggling from cost
	// to tokens and reaching Phase 2, the label format shows token counts (no "$").
	m := seedTwoPhaseAnimationModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	// Drive ticks until we cross into Phase 2.
	const maxTicks = 100
	for i := 0; i < maxTicks && m.springPhase != springGrowing; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springPhase != springGrowing {
		t.Fatalf("never reached Phase 2 in %d ticks", maxTicks)
	}

	// Drive a few more ticks so the spring grows enough that maxRatio
	// is above the fade-stop-1 threshold (≥ 0.2). At V0=5, omega=6 the
	// spring reaches ~0.5 in a few ticks for non-zero targets.
	for i := 0; i < 10 && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatalf("animation ended before we could sample Phase 2 mid-frame")
	}

	body := strings.Join(chartBodyLines(m.View()), "\n")
	// Phase 2 should expose a token-shaped label (contains "k" or "M", no "$") in the chart body.
	if strings.Contains(body, "$") || (!strings.Contains(body, "k") && !strings.Contains(body, "M")) {
		t.Errorf("Phase 2 chart body should show NEW (tokens) Y-label without '$'; got body:\n%s", body)
	}
}

func TestLabelFade_SyncedWithMaxRatio(t *testing.T) {
	// View()'s computed fade must equal max(springRatios) clamped to
	// [0, 1] while the animation is active. We assert indirectly by
	// inspecting the rendered View at known animation states: at the
	// empty-moment frame (just after Phase 1 handoff, springRatios all
	// zero), the Y-label must be absent. At steady state (springActive
	// = false), the Y-label must be present.
	m := seedTwoPhaseAnimationModel(t)

	// Pre-toggle baseline: the Y-label is present at steady state (cost mode by default).
	pre := m.View()
	if !strings.Contains(pre, "$") {
		t.Fatalf("baseline View has no cost-shaped label ('$'); test setup wrong:\n%s", pre)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	// Drive ticks until the Hold phase (ratios snapped to zero, no Phase
	// 2 grow tick yet). The growing-Phase Update will fire on the next
	// tick after the hold tick arrives (#163).
	const maxTicks = 50
	for i := 0; i < maxTicks && m.springPhase == springShrinking; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springPhase != springHolding {
		t.Fatalf("did not reach Hold phase in %d ticks, got phase %d", maxTicks, m.springPhase)
	}

	// At this exact moment springRatios were snapped to zero but no
	// growing-Phase tick has run yet. Verify the invariant the empty
	// frame depends on:
	for i, r := range m.springRatios {
		if r != 0 {
			t.Fatalf("springRatios[%d] = %v at handoff, want 0", i, r)
		}
	}

	emptyMomentView := m.View()
	// At the empty moment, the Y-label is absent — overlayYLabel returned
	// body unchanged because fade <= 0. The chart-body region should not
	// contain "$" or "k" Y-label glyphs.
	for _, line := range chartBodyLines(emptyMomentView) {
		if strings.Contains(line, "$") {
			t.Errorf("empty-moment frame contains '$' in body line: %q", line)
		}
	}
}

// seedTwoPhaseAnimationModel constructs a Model with two fixture
// messages so toggling 'u' kicks off a two-phase animation. Cache is
// closed in a t.Cleanup.
func seedTwoPhaseAnimationModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()
	// seedTwoPhaseAnimationModel represents post-open state: refreshChart
	// has been called outside the WindowSizeMsg handler, so the open-path
	// intro is treated as already fired/settled. Without this, every
	// RefreshMsg / WindowSizeMsg in unit-toggle tests would re-arm the
	// intro after the existing handler aborts the unit spring. See #188.
	m.introPending = false
	return m
}

// chartBodyLines returns the chart-body rows from a rendered View.
// Filters out header (5h / 7d quota rows + border lines) and footer
// (keybinding help, indicators) so substring checks on the Y-label
// or chart cells aren't false-positively poisoned by header changes.
func chartBodyLines(view string) []string {
	t := []string{}
	for line := range strings.SplitSeq(view, "\n") {
		if strings.ContainsAny(line, "─│") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "5h") ||
			strings.HasPrefix(trimmed, "7d") ||
			strings.HasPrefix(trimmed, "burn") {
			continue
		}
		// Footer: starts with help keybinding hints like "q quit" or
		// "?  toggle help" — these always contain the literal string
		// "quit" since the binding is fixed.
		if strings.Contains(line, "quit") {
			continue
		}
		t = append(t, line)
	}
	return t
}

// quotaBarLines returns the rendered bars-row line(s) from a View.
// The 5h and 7d quota bars are joined horizontally inside the bordered
// header box, so a single line normally contains both sides — that
// line is what we return. Strips the leading box-border char ('│') and
// padding whitespace before matching the "5h"/"7d" label prefix so the
// lipgloss-rendered border doesn't defeat HasPrefix.
func quotaBarLines(view string) []string {
	var rows []string
	for line := range strings.SplitSeq(view, "\n") {
		trimmed := strings.TrimLeft(line, "│ \t")
		if strings.HasPrefix(trimmed, "5h") || strings.HasPrefix(trimmed, "7d") {
			rows = append(rows, line)
		}
	}
	return rows
}

func TestRefreshMsg_AbortsBothPhases(t *testing.T) {
	// RefreshMsg arriving in either phase must hard-cut the animation:
	// springActive=false and springPhase=springIdle. Driven by the
	// existing refreshChart chokepoint (Task 7 extends it).
	cases := []struct {
		name  string
		phase springPhase
	}{
		{"Phase1", springShrinking},
		{"Hold", springHolding},
		{"Phase2", springGrowing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := seedTwoPhaseAnimationModel(t)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
			m = updated.(Model)

			// Drive ticks until we reach the target phase. Phase 1 is the
			// initial state after 'u', so the loop is a no-op for it.
			const maxTicks = 100
			for i := 0; i < maxTicks && m.springPhase != tc.phase; i++ {
				updated, _ = m.Update(springTickMsg{gen: m.springGen})
				m = updated.(Model)
			}
			if m.springPhase != tc.phase {
				t.Fatalf("never reached %s in %d ticks", tc.name, maxTicks)
			}

			// Now in the target phase. Deliver RefreshMsg.
			updated, _ = m.Update(RefreshMsg{})
			m = updated.(Model)

			if m.springActive {
				t.Errorf("springActive = true after RefreshMsg in %s; expected hard-cut", tc.name)
			}
			if m.springPhase != springIdle {
				t.Errorf("springPhase = %d after RefreshMsg in %s; expected springIdle", m.springPhase, tc.name)
			}
		})
	}
}

func TestVisualProbe_PhaseHandoffIsClean(t *testing.T) {
	// In-process visual probe: drive the animation to the exact
	// handoff moment (springRatios all zero, springPhase just flipped
	// to springGrowing). The chart body in the rendered View() must
	// have NO coloured bar cells — every chart row is uniform spaces
	// inside the chart-cell region. (The header and footer remain;
	// we only inspect chart rows.)
	m := seedTwoPhaseAnimationModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	const maxTicks = 50
	for i := 0; i < maxTicks && m.springPhase == springShrinking; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	// Loop exits at the Phase 1 → Hold handoff. springRatios are snapped
	// to zero; no Phase 2 grow tick has run yet. This is exactly the
	// "empty moment" the probe is asserting on (#163).
	if m.springPhase != springHolding {
		t.Fatalf("did not reach Hold phase in %d ticks, got phase %d", maxTicks, m.springPhase)
	}
	// At this moment springRatios were just snapped to 0; no growing
	// tick has yet rendered new bars.
	for i, r := range m.springRatios {
		if r != 0 {
			t.Fatalf("springRatios[%d] = %v at handoff, want 0", i, r)
		}
	}

	view := m.View()
	// ANSI bar cells inside the chart use SGR colour escapes (unit-keyed
	// color + lipgloss). The empty-moment frame must contain NO foreground-
	// background SGR pairs in the chart-body region.
	//
	// Heuristic: count the number of chart cells (█ or other heavy
	// glyphs) in the body. With springRatios all zero and maxValue=1
	// passed to ntcharts, no cells should be drawn.
	if strings.ContainsAny(view, "█▇▆▅▄▃▂▁") {
		t.Errorf("empty-moment frame contains chart-cell glyphs:\n%s", view)
	}
}

func TestBeginUnitAnimation_EmptyCache(t *testing.T) {
	// First 'u' toggle on a model whose cache is empty: beginUnitAnimation
	// runs refreshChart, which short-circuits on the EarliestMessageTime
	// missing-row path, leaving lastValues empty. The empty-newValues
	// guard inside beginUnitAnimation must then set springActive=false
	// AND springPhase=springIdle without allocating any spring slices.
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	// Pretend the user toggled the unit (Update's keybinding already
	// increments unitIdx; replicate that here to exercise the same path).
	m.unitIdx = int(chartUnitCost)
	m.beginUnitAnimation()

	if m.springActive {
		t.Errorf("springActive = true on empty-cache toggle; expected no animation")
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d; expected springIdle", m.springPhase)
	}
	if len(m.springs) != 0 || len(m.springProjectiles) != 0 {
		t.Errorf("spring slices allocated on empty-cache toggle (springs=%d, projectiles=%d); expected zero",
			len(m.springs), len(m.springProjectiles))
	}
}

func TestUnitKey_ReduceMotion_EmptyCache(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	m := New(Deps{Cache: c, ReduceMotion: true})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	if cmd != nil {
		t.Errorf("cmd != nil; expected nil (no tick scheduled in reduce-motion path)")
	}
	if m.springActive {
		t.Errorf("springActive = true; expected false in reduce-motion path")
	}
	if m.unitIdx != 1 {
		t.Errorf("unitIdx = %d; expected 1 after one toggle", m.unitIdx)
	}
	if len(m.lastValues) != 0 {
		t.Errorf("len(lastValues) = %d; expected 0 on empty cache", len(m.lastValues))
	}
}

func TestWindowSizeMsg_AbortsAnimation(t *testing.T) {
	// WindowSizeMsg routes through refreshChart, which clears both
	// springActive and springPhase. Spec acceptance criteria explicitly
	// calls out WindowSizeMsg alongside RefreshMsg and Zoom as abort
	// triggers, so test it directly rather than relying on transitive
	// coverage via TestRefreshMsg_AbortsBothPhases.
	for _, phase := range []springPhase{springShrinking, springGrowing} {
		name := "Phase1"
		if phase == springGrowing {
			name = "Phase2"
		}
		t.Run(name, func(t *testing.T) {
			m := seedTwoPhaseAnimationModel(t)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
			m = updated.(Model)

			if phase == springGrowing {
				const maxTicks = 100
				for i := 0; i < maxTicks && m.springPhase != springGrowing; i++ {
					updated, _ = m.Update(springTickMsg{gen: m.springGen})
					m = updated.(Model)
				}
				if m.springPhase != springGrowing {
					t.Fatalf("never reached Phase 2 in %d ticks", maxTicks)
				}
			}

			updated, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			m = updated.(Model)

			if m.springActive {
				t.Errorf("springActive = true after WindowSizeMsg in %s; expected hard-cut", name)
			}
			if m.springPhase != springIdle {
				t.Errorf("springPhase = %d after WindowSizeMsg in %s; expected springIdle", m.springPhase, name)
			}
		})
	}
}

func TestLabelFade_MidAnimationBinding(t *testing.T) {
	// The Y-label's fade level must follow max(springRatios) — high max
	// renders the label at full opacity (no Foreground), low max renders
	// it in a near-background grey, and max=0 omits the label entirely.
	// We verify the direction binding directly: under forced TrueColor,
	// fade=1.0 produces a label with no SGR wrapping, whereas a fade
	// strictly less than 1.0 produces an SGR-wrapped label. The other
	// fade-related tests cover the empty-moment and content swap; this
	// test pins the brightness binding.
	withForcedColor(t)
	withForcedDarkBackground(t, true)

	const probe = "$1.23"
	full := labelFadeStyle(1.0).Render(probe)
	if full != probe {
		t.Errorf("labelFadeStyle(1.0).Render = %q, want %q (fade=1.0 must be full opacity = no Foreground)",
			full, probe)
	}

	// Sample two distinct sub-full fade levels and assert they BOTH
	// produce SGR-wrapped output that differs from the full-opacity
	// rendering. Avoids hard-coding hex bytes that termenv might
	// downsample on this platform.
	for _, fade := range []float64{0.5, 0.1} {
		got := labelFadeStyle(fade).Render(probe)
		if got == probe {
			t.Errorf("labelFadeStyle(%v).Render = %q (no SGR); expected SGR wrapping at sub-full fade",
				fade, got)
		}
		if got == full {
			t.Errorf("labelFadeStyle(%v).Render matches labelFadeStyle(1.0); expected distinct styling",
				fade)
		}
	}
}

func TestVisibleBuckets_24hAtTinyWidths(t *testing.T) {
	t.Parallel()
	// 24h has BarWidth=10. chartWidth() floors at 10, so visibleBuckets
	// is at least 1 at any terminal width. This guards the
	// "never collapse to zero visible bars" invariant.
	m := Model{w: 12, zoomIdx: 2} // chartWidth() = 10; 10/10 = 1
	if got := m.visibleBuckets(); got != 1 {
		t.Errorf("visibleBuckets w=12 24h = %d, want 1", got)
	}

	m = Model{w: 6, zoomIdx: 2} // chartWidth floors at 10; 10/10 = 1
	if got := m.visibleBuckets(); got != 1 {
		t.Errorf("visibleBuckets tiny w 24h = %d, want 1", got)
	}
}

func TestVisibleBuckets_BarWidthOne(t *testing.T) {
	t.Parallel()
	m := Model{w: 22, zoomIdx: 0} // 15m, BarWidth=1; chartWidth() = 20
	if got := m.visibleBuckets(); got != 20 {
		t.Errorf("visibleBuckets w=22 15m = %d, want 20", got)
	}
}

// TestRenderSpringFrame_MatchesPreSpringBoundary asserts that
// renderSpringFrame with identity ratios (springRatios[i] = lastValues[i] /
// peak) produces a viewport.View() that matches the pre-spring viewport
// content set up by buildChart + setX at the same scroll position.
//
// At 24h zoom (BarGap=2, BarWidth=10, stride=12) the canvas right edge is
// rarely a stride-multiple, so viewport.SetXOffset is clamped by the
// longestLineWidth-Width boundary. The tested widths were chosen to exercise
// the three slack variants that caused bugs before commit fa365d8:
//
//	w=122 → slack=2 (leading gap between buckets)
//	w=130 → slack=10 (leading partial bar)
//	w=131 → slack=11 (mostly-consumed partial bar)
func TestRenderSpringFrame_MatchesPreSpringBoundary(t *testing.T) {
	t.Parallel()
	const (
		N       = 60  // number of 24h buckets
		zoomIdx = 2   // 24h zoom: BarWidth=10, BarGap=2, stride=12
		chartH  = 20  // representative chart height
	)

	zoom := ZoomLevels[zoomIdx]

	// Build deterministic synthetic values: bucket i gets value (i+1)*100,
	// so peak = N*100 and every bucket has a non-zero distinct height.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lastValues := make([]float64, N)
	lastStarts := make([]time.Time, N)
	var peak float64
	for i := range N {
		lastValues[i] = float64((i + 1) * 100)
		lastStarts[i] = now.AddDate(0, 0, i)
		if lastValues[i] > peak {
			peak = lastValues[i]
		}
	}

	cases := []struct {
		name       string
		w          int
		wantSlack  int // expected leading blank cols in pre-spring view
	}{
		{name: "slack=2_leading_gap", w: 122},
		{name: "slack=10_partial_bar", w: 130},
		{name: "slack=11_mostly_partial", w: 131},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// ── Build model fields (no cache needed) ──────────────────────────
			m := New(Deps{}) // deps.Cache = nil; we set fields manually
			m.w = tc.w
			m.h = chartH + 7 // chartHeight() = h-7, so h = chartH+7
			m.zoomIdx = zoomIdx
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()

			m.peak = peak
			m.lastValues = append([]float64(nil), lastValues...)
			m.lastStarts = append([]time.Time(nil), lastStarts...)

			// ── Pre-spring viewport: exactly what refreshChart produces ────────
			// refreshChart calls buildChart on the full canvas then setX(len(values))
			// which pins to the right edge.
			canvasW := zoom.CanvasWidth(N)
			m.viewport.SetContent(buildChart(
				m.lastValues, m.lastStarts, peak,
				canvasW, m.chartHeight(), now, zoom, chartUnitTokens, dateOrderMonthFirst,
			))
			m.setX(len(m.lastValues)) // pins to right edge

			preSpringView := m.viewport.View()

			// Sanity: pre-spring view must not be all-blank — we seeded real data.
			if strings.TrimSpace(stripANSIForTest(preSpringView)) == "" {
				t.Fatalf("pre-spring viewport unexpectedly blank (setup error?)")
			}

			// ── Spring setup: identity ratios (old values / niceCeiling(peak)) ──
			// After #250, buildChart's maxValue = niceCeilingFloat(peak), so
			// spring ratios must use the same denominator to match pre-spring
			// bar heights.
			// springActive must be true so renderSpringFrame doesn't early-return.
			// springXOffset is the viewportXOffset (the pinned-right bucket index).
			n := len(m.lastValues)
			m.springRatios = make([]float64, n)
			for i := range n {
				m.springRatios[i] = m.lastValues[i] / niceCeilingFloat(peak)
			}
			m.springActive = true
			m.springXOffset = m.viewportXOffset // shadow set by setX

			// ── Spring frame ──────────────────────────────────────────────────
			m.renderSpringFrame()
			springView := m.viewport.View()

			// ── Assert equality (ANSI-stripped, per-line, full bar rows) ────
			// Skip the last row: it's the X-label row, which both pre-spring
			// and spring compute via formatXLabel(bucket, zoom, time.Now(), …).
			// Pre-spring uses the `now` constant above; renderSpringFrame
			// calls time.Now() internally. The two timestamps are close but
			// not identical, so day-boundary edge cases (weekday vs. MM/DD)
			// can legitimately differ by one label format. The invariant we're
			// locking is bar-height alignment, not X-label text.
			preLines := strings.Split(stripANSIForTest(preSpringView), "\n")
			sprLines := strings.Split(stripANSIForTest(springView), "\n")

			if len(preLines) != len(sprLines) {
				t.Fatalf("line count mismatch: pre-spring=%d spring=%d", len(preLines), len(sprLines))
			}

			// Bar rows are all rows except the last (X-labels).
			barRowCount := len(preLines) - 1
			if barRowCount < 1 {
				t.Fatalf("no bar rows to compare (chartH too small?)")
			}

			// Compare each bar row in full. The bug class shifts every bar by
			// a full stride; truncation isn't needed to catch it, and byte-
			// slicing multi-byte UTF-8 block runes (█, ▄, etc.) is unsafe.
			for i := range barRowCount {
				pre := strings.TrimRight(preLines[i], " ")
				spr := strings.TrimRight(sprLines[i], " ")
				if pre != spr {
					t.Errorf("subtest %s: bar row %d mismatch\n pre: %q\nspring: %q",
						tc.name, i, pre, spr)
				}
			}
		})
	}
}

func TestRefreshChart_RemainingMode(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 10; i++ {
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: float64(i * 10), ResetsAt: timePtr(now.Add(time.Hour))},
			SevenDay: &anthro.Bucket{Utilization: float64(i * 5), ResetsAt: timePtr(now.Add(24 * time.Hour))},
		}
		if err := c.RecordUsageSample(u, now.Add(time.Duration(-i)*3*time.Minute)); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}

	tab, _ := pricing.Load()
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 100,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.unitIdx = int(chartUnitRemaining)
	m.refreshChart()

	if m.peak != 1.0 {
		t.Errorf("peak = %f, want 1.0", m.peak)
	}
	if len(m.lastPts5h) == 0 {
		t.Error("expected non-empty lastPts5h")
	}
	view := m.View()
	if view == "" {
		t.Error("View returned empty string in remaining mode")
	}
}

func TestBeginUnitAnimation_BarToLine(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now().UTC().Truncate(time.Minute)
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 5000,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 50.0, ResetsAt: timePtr(now.Add(time.Hour))},
		SevenDay: &anthro.Bucket{Utilization: 25.0, ResetsAt: timePtr(now.Add(24 * time.Hour))},
	}
	if err := c.RecordUsageSample(u, now); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart() // tokens mode

	// Toggle to cost (bar→bar), then to remaining (bar→line).
	m.unitIdx = int(chartUnitCost)
	m.refreshChart()
	m.unitIdx = int(chartUnitRemaining)
	m.beginUnitAnimation()

	if !m.springActive {
		t.Fatal("expected springActive=true after bar→line toggle")
	}
	if m.springPhase != springShrinking {
		t.Errorf("expected springShrinking, got %d", m.springPhase)
	}
	if !m.newIsLine {
		t.Error("expected newIsLine=true")
	}
	if m.oldIsLine {
		t.Error("expected oldIsLine=false")
	}
}

func TestBeginUnitAnimation_LineToBar(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now().UTC().Truncate(time.Minute)
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 5000, OutputTokens: 2000,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 50.0, ResetsAt: timePtr(now.Add(time.Hour))},
	}
	if err := c.RecordUsageSample(u, now); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Start in remaining mode.
	m.unitIdx = int(chartUnitRemaining)
	m.refreshChart()

	// Toggle to cost — the forward step from remaining(2) is cost(0), not tokens(1).
	// beginUnitAnimation derives oldUnitIdx via (newIdx+count-1)%count, so
	// cost(0) → oldUnitIdx=(0+2)%3=2=remaining (line). Line→bar transition.
	m.unitIdx = int(chartUnitCost)
	m.beginUnitAnimation()

	if !m.springActive {
		t.Fatal("expected springActive=true")
	}
	if !m.oldIsLine {
		t.Error("expected oldIsLine=true")
	}
	if m.newIsLine {
		t.Error("expected newIsLine=false")
	}
}

func TestView_RemainingModeShowsYTicks(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now().UTC().Truncate(time.Minute)
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 5000,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 40.0, ResetsAt: timePtr(now.Add(time.Hour))},
		SevenDay: &anthro.Bucket{Utilization: 20.0, ResetsAt: timePtr(now.Add(24 * time.Hour))},
	}
	if err := c.RecordUsageSample(u, now); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	m.unitIdx = int(chartUnitRemaining)
	m.refreshChart()

	view := m.View()
	if !strings.Contains(view, "100%") {
		t.Error("View in remaining mode should contain 100% Y-tick")
	}
	if !strings.Contains(view, "0%") {
		t.Error("View in remaining mode should contain 0% Y-tick")
	}
}

// TestFullUnitCycle_CostTokensRemaining verifies that three presses of the
// 'u' key cycle through cost → tokens → remaining → cost, and that each
// mode produces a non-empty view with the expected mode-specific marker.
// TestRenderSpringFrame_LineBranchUsesOldSnapshot verifies that the line-
// rendering branch of renderSpringFrame reads from m.oldPts5h / m.oldPts7d
// (the snapshot taken before refreshChart) rather than m.lastPts5h /
// m.lastPts7d (which refreshChart overwrites with the new unit's data).
//
// Differentiation strategy: run renderSpringFrame twice.
//   - Run A: oldPts5h populated with real data, lastPts5h nil.
//   - Run B: both oldPts5h and lastPts5h nil.
//
// If the branch correctly reads oldPts*, Run A renders the real line shape
// and differs from Run B (which renders the empty/flat fallback). If the
// branch incorrectly reads lastPts* (the bug), both runs would produce the
// same flat output — and the test would fail with "viewA == viewB".
//
// This avoids replicating the interpPt math and doesn't depend on exact
// time values inside renderSpringFrame, which calls time.Now() internally.
func TestRenderSpringFrame_LineBranchUsesOldSnapshot(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now().UTC().Truncate(time.Minute)

	// Insert one message so EarliestMessageTime returns a value.
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 5000,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// buildModel returns a fresh model in remaining mode with the spring
	// wired into the springShrinking phase and oldIsLine=true (exit of a
	// line chart toward a bar chart — the scenario that must use oldPts*).
	buildModel := func() Model {
		m := New(Deps{Cache: c})
		m.w, m.h = 120, 40
		m.viewport.Width = m.chartWidth()
		m.viewport.Height = m.chartHeight()

		// Use explicit, fixed time windows so both builds use identical
		// from/to values. renderSpringFrame uses m.lastChartFrom/To directly
		// (falling back to time.Now() only when zero), so setting them
		// avoids any time-drift between the two render calls.
		m.lastChartFrom = now.Add(-5 * time.Hour)
		m.lastChartTo = now

		// springRatios must be non-empty to pass the early-return guard.
		// Values in (0, 1] so maxR > 0 and interpPt produces a non-trivial
		// result when pts are populated.
		m.springRatios = []float64{0.8, 0.6, 0.7}

		// Simulate mid-exit animation: old chart was a line, new is a bar.
		m.springActive = true
		m.springPhase = springShrinking
		m.oldIsLine = true
		m.newIsLine = false

		// lastPts* deliberately nil — refreshChart on the new unit would have
		// cleared or replaced these with bar data. The test must pass even
		// when lastPts* is empty (i.e. the branch must NOT fall through to it).
		m.lastPts5h = nil
		m.lastPts7d = nil

		return m
	}

	// Synthetic old points with distinct Pct values so the rendered line
	// shape is non-trivial. Values cover a range that interpPt maps onto
	// visible line positions.
	oldPts := []cache.UtilizationPoint{
		{At: now.Add(-4 * time.Hour), Pct: 20.0},
		{At: now.Add(-2 * time.Hour), Pct: 50.0},
		{At: now.Add(-1 * time.Hour), Pct: 80.0},
	}

	// ── Run A: oldPts populated, lastPts nil ──────────────────────────────
	mA := buildModel()
	mA.oldPts5h = append([]cache.UtilizationPoint(nil), oldPts...)
	mA.oldPts7d = append([]cache.UtilizationPoint(nil), oldPts...)
	// Set a non-zero XOffset before the call to verify the line branch resets it.
	mA.viewport.SetXOffset(42)
	mA.renderSpringFrame()
	viewA := mA.viewport.View()

	// ── Run B: both oldPts and lastPts nil ────────────────────────────────
	mB := buildModel()
	mB.oldPts5h = nil
	mB.oldPts7d = nil
	mB.viewport.SetXOffset(42)
	mB.renderSpringFrame()
	viewB := mB.viewport.View()

	// ── Assertions ────────────────────────────────────────────────────────

	// The line branch must produce non-empty output in Run A (populated pts).
	if strings.TrimSpace(stripANSIForTest(viewA)) == "" {
		t.Fatal("viewA is blank; renderSpringFrame line branch produced no output with populated oldPts")
	}

	// viewA (populated oldPts) and viewB (nil oldPts) must differ.
	// If the branch incorrectly reads lastPts* (nil in both runs), both
	// would produce the same flat/empty fallback, and this assertion fails.
	if stripANSIForTest(viewA) == stripANSIForTest(viewB) {
		t.Error("viewA == viewB: line branch did not distinguish populated oldPts from nil; " +
			"likely reads lastPts* instead of oldPts* during springShrinking")
	}

	// The line branch calls viewport.SetXOffset(0), which makes the leading
	// content visible. The shadow viewportXOffset is only updated by setX,
	// not by renderSpringFrame, so we verify this indirectly: the rendered
	// content must be non-empty (a non-zero XOffset beyond the canvas width
	// would blank the viewport). Already covered by the non-empty check above.
}

func TestFullUnitCycle_CostTokensRemaining(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	now := time.Now().UTC().Truncate(time.Minute)
	msgs := []parse.Message{{
		SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
		Timestamp: now.Add(-30 * time.Minute), InputTokens: 5000,
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 40.0, ResetsAt: timePtr(now.Add(time.Hour))},
		SevenDay: &anthro.Bucket{Utilization: 20.0, ResetsAt: timePtr(now.Add(24 * time.Hour))},
	}
	if err := c.RecordUsageSample(u, now); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	pressU := func() {
		model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
		m = model.(Model)
		// Abort any in-flight spring so View() shows the settled new state.
		m.springActive = false
		m.springPhase = springIdle
		m.refreshChart()
	}

	// Press 1: cost (0) → tokens (1). Tokens-mode view has the bare
	// k/M-formatted Y-overlay (no '$', no '100%').
	pressU()
	if m.unitIdx != int(chartUnitTokens) {
		t.Fatalf("after 1st press: want unitIdx=%d (tokens), got %d", int(chartUnitTokens), m.unitIdx)
	}
	view1 := m.View()
	if strings.Contains(view1, "$") {
		t.Errorf("tokens mode view must not contain '$':\n%s", view1)
	}

	// Press 2: tokens (1) → remaining (2). Remaining view is a 0-100% line chart.
	pressU()
	if m.unitIdx != int(chartUnitRemaining) {
		t.Fatalf("after 2nd press: want unitIdx=%d (remaining), got %d", int(chartUnitRemaining), m.unitIdx)
	}
	view2 := m.View()
	if !strings.Contains(view2, "100%") {
		t.Errorf("remaining mode view should contain '100%%':\n%s", view2)
	}

	// Press 3: remaining (2) → cost (0). Back to start; cost view contains '$'.
	pressU()
	if m.unitIdx != int(chartUnitCost) {
		t.Fatalf("after 3rd press: want unitIdx=%d (cost), got %d", int(chartUnitCost), m.unitIdx)
	}
	view3 := m.View()
	if !strings.Contains(view3, "$") {
		t.Errorf("cost mode view should contain '$':\n%s", view3)
	}
}

func TestRefreshChart_EmptyCacheRecoveryPinsRight(t *testing.T) {
	// Guards the regression from issue #179: an empty-cache early-return in
	// refreshChart left stale lastCanvasW/lastChartFrom/lastChartTo/lastZoomStride
	// on the model. When data returned on the next refresh, the anchor logic
	// saw a non-zero lastCanvasW and treated the offset as a wall-clock anchor
	// (hadAnchor=true), mapping the stale position to the LEFT of the new
	// canvas instead of pinning to the right edge.
	m, cleanup := seedScrollTestModel(t, 200)
	defer cleanup()
	m.unitIdx = int(chartUnitTokens)

	seeded := m.deps.Cache

	// Swap in an empty cache to trigger the EarliestMessageTime no-data branch.
	empty, err := cache.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	m.deps.Cache = empty

	m.refreshChart()

	// After the empty-cache refresh all canvas-state fields must be zeroed.
	if m.lastCanvasW != 0 {
		t.Errorf("lastCanvasW = %d after empty-cache refresh; want 0", m.lastCanvasW)
	}
	if !m.lastChartFrom.IsZero() {
		t.Errorf("lastChartFrom = %v after empty-cache refresh; want zero", m.lastChartFrom)
	}

	// Restore the seeded cache and force viewport to left edge so we can
	// detect whether the next refresh correctly re-pins to the right.
	m.deps.Cache = seeded
	m.setX(0)

	m.refreshChart()

	// The recovery refresh must treat this as a first-load (hadAnchor=false)
	// and pin to the right edge.
	stride := m.lastZoomStride
	if stride == 0 {
		stride = 1
	}
	wantOffset := max(0, m.lastCanvasW-m.viewport.Width) / stride
	if got := m.viewportXOffset; got != wantOffset {
		t.Errorf("viewportXOffset = %d after empty-cache recovery; want right edge %d (lastCanvasW=%d, viewportWidth=%d, stride=%d)",
			got, wantOffset, m.lastCanvasW, m.viewport.Width, stride)
	}
}

func TestNew_IntroPendingDefaults(t *testing.T) {
	// New() must initialise introPending = !ReduceMotion so the open-path
	// slide-in is enabled by default but disabled when reduce_motion=true.
	cases := []struct {
		name         string
		reduceMotion bool
		want         bool
	}{
		{"motion_on", false, true},
		{"reduce_motion", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Deps{ReduceMotion: tc.reduceMotion})
			if m.introPending != tc.want {
				t.Errorf("introPending = %v after New(ReduceMotion=%v); want %v",
					m.introPending, tc.reduceMotion, tc.want)
			}
		})
	}
}

// seedIntroModel returns a freshly New()-constructed model with a
// non-empty cache but BEFORE any WindowSizeMsg or refreshChart, so
// the test can deliver the WindowSizeMsg itself and observe the
// intro-arm path end-to-end. Mirror of seedTwoPhaseAnimationModel
// minus the w/h setup and the refreshChart call.
func seedIntroModel(t *testing.T, reduceMotion bool) Model {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	return New(Deps{Cache: c, ReduceMotion: reduceMotion})
}

func TestIntro_ReduceMotion_SnapsWithoutTick(t *testing.T) {
	// With ReduceMotion=true, the first WindowSizeMsg must:
	//   - leave springActive=false (no animation),
	//   - return cmd=nil (no tick),
	//   - leave introPending=false (initialised that way by New()).
	m := seedIntroModel(t, true)
	if m.introPending {
		t.Fatalf("introPending = true after New(ReduceMotion=true); want false (sanity)")
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	if cmd != nil {
		t.Errorf("cmd = %v after first WindowSizeMsg; want nil (no tick in reduce_motion)", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true; want false (no animation in reduce_motion)")
	}
	if m.introPending {
		t.Errorf("introPending = true after first WindowSizeMsg; want false (still cleared)")
	}
	if len(m.lastValues) == 0 {
		t.Errorf("lastValues empty after WindowSizeMsg; want non-empty (refreshChart should have populated it)")
	}
}

func TestIntro_HoldFrameRendersZeroBars(t *testing.T) {
	// First WindowSizeMsg with non-empty cache + motion-on must:
	//   - arm the intro (springActive=true, springPhase=springHolding),
	//   - return a non-nil Cmd (the hold tick),
	//   - leave all springRatios at zero (hold frame),
	//   - render a chart body with no visible bar block characters.
	m := seedIntroModel(t, false)
	if !m.introPending {
		t.Fatalf("introPending = false after New(ReduceMotion=false); want true (sanity)")
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	if cmd == nil {
		t.Fatalf("cmd = nil after first WindowSizeMsg; want non-nil hold tick")
	}
	if !m.springActive {
		t.Errorf("springActive = false after WindowSizeMsg; want true (intro armed)")
	}
	if m.springPhase != springHolding {
		t.Errorf("springPhase = %d after WindowSizeMsg; want springHolding (%d)", m.springPhase, springHolding)
	}
	if m.introPending {
		t.Errorf("introPending = true after intro arm; want false (one-shot)")
	}
	for i, r := range m.springRatios {
		if r != 0 {
			t.Errorf("springRatios[%d] = %v after intro arm; want 0 (hold frame)", i, r)
			break
		}
	}

	// Visual assertion: the chart body must contain no bar block characters.
	// ntcharts/barchart uses block elements ('▁'..'█'); a zero-height chart
	// leaves the body region empty (only whitespace).
	body := chartBodyLines(m.View())
	for _, line := range body {
		if strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
			t.Errorf("chart body contains bar block char during hold frame: %q", line)
			break
		}
	}
}

func TestIntro_GrowLadderSettles(t *testing.T) {
	// After the hold tick, the intro must traverse springHolding →
	// springGrowing and settle within the same tick budget as the
	// existing unit-toggle Phase 2.
	m := seedIntroModel(t, false)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("WindowSizeMsg returned nil cmd; intro did not arm")
	}

	// First springTickMsg drives the springHolding → springGrowing
	// transition; subsequent ticks run the FPS grow loop.
	const maxTicks = 200
	var lastCmd tea.Cmd
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, lastCmd = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	if m.springPhase != springIdle {
		t.Errorf("springPhase = %d after settle; want springIdle", m.springPhase)
	}
	for i, r := range m.springRatios {
		if r != m.springTargetRatios[i] {
			t.Errorf("springRatios[%d] = %v after settle; want %v (snapped target)",
				i, r, m.springTargetRatios[i])
		}
	}
	if lastCmd != nil {
		t.Errorf("settle tick returned non-nil Cmd; idle TUI must not keep ticking")
	}
}

func TestIntro_OneShot_NoReArmOnSecondWindowSize(t *testing.T) {
	// Once the intro has fired (whether settled or hard-cut), a second
	// WindowSizeMsg must not re-arm it. introPending is cleared on the
	// first arm and never re-set.
	m := seedIntroModel(t, false)

	// First WindowSizeMsg arms the intro.
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("first WindowSizeMsg returned nil cmd; intro did not arm")
	}

	// Settle the animation.
	const maxTicks = 200
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}

	// Second WindowSizeMsg must return nil and leave spring state idle.
	updated, cmd2 := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m = updated.(Model)
	if cmd2 != nil {
		t.Errorf("second WindowSizeMsg returned non-nil cmd; intro must not re-arm (cmd = %v)", cmd2)
	}
	if m.springActive {
		t.Errorf("springActive = true after second WindowSizeMsg; want false (idle)")
	}
}

func TestIntro_RefreshMsgBeforeWindowSizeMsg(t *testing.T) {
	// Production startup ordering: cmd/ccpulse/main.go:329 fires
	// p.Send(tui.RefreshMsg{}) in a goroutine, which often arrives
	// BEFORE bubbletea's initial WindowSizeMsg. Confirmed via
	// runtime trace. If maybeArmIntro armed at m.w=0, the subsequent
	// WindowSizeMsg's refreshChart would tear down the zero-size
	// spring frame via the spring-abort block and the intro would be
	// invisible. Guarded by the m.w == 0 check in maybeArmIntro.
	m := seedIntroModel(t, false)
	if m.w != 0 {
		t.Fatalf("seedIntroModel pre-condition: m.w = %d; want 0", m.w)
	}

	// Pre-WindowSizeMsg RefreshMsg — must NOT arm.
	updated, cmd := m.Update(RefreshMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("RefreshMsg before WindowSizeMsg returned non-nil cmd; want nil (deferred)")
	}
	if m.springActive {
		t.Errorf("springActive = true after pre-WindowSize RefreshMsg; want false (deferred)")
	}
	if !m.introPending {
		t.Errorf("introPending = false after pre-WindowSize RefreshMsg; want true (still pending)")
	}

	// Now WindowSizeMsg — must arm.
	updated, cmd = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("WindowSizeMsg after deferred RefreshMsg returned nil cmd; want hold tick")
	}
	if !m.springActive {
		t.Errorf("springActive = false after WindowSizeMsg; want true (intro armed)")
	}
	if m.springPhase != springHolding {
		t.Errorf("springPhase = %d after WindowSizeMsg; want springHolding", m.springPhase)
	}
	if m.introPending {
		t.Errorf("introPending = true after intro arm; want false (one-shot)")
	}
}

func TestIntro_SurvivesInitialRefreshMsgRace(t *testing.T) {
	// Real-world startup race: WindowSizeMsg arms the intro, then the
	// initial RefreshMsg from main.go (cmd/ccpulse/main.go:329) fires
	// before the first spring tick. refreshChart()'s spring-abort
	// logic (model.go:1079) would kill the intro if not guarded.
	//
	// Visual probe: the chart body must still contain no bar block
	// characters after the RefreshMsg — i.e. the viewport is still
	// showing the zero-height hold frame, not the fully-painted chart
	// that refreshChart would otherwise paint.
	m := seedIntroModel(t, false)

	// Arm intro via WindowSizeMsg.
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("WindowSizeMsg returned nil cmd; intro did not arm")
	}
	if !m.springActive || m.springPhase != springHolding {
		t.Fatalf("intro not armed: springActive=%v springPhase=%d", m.springActive, m.springPhase)
	}

	// Deliver RefreshMsg BEFORE any springTickMsg — this is the race.
	updated, _ = m.Update(RefreshMsg{})
	m = updated.(Model)

	if !m.springActive {
		t.Errorf("springActive = false after RefreshMsg race; intro was killed")
	}
	if m.springPhase != springHolding {
		t.Errorf("springPhase = %d after RefreshMsg race; want springHolding (%d)", m.springPhase, springHolding)
	}

	// Visual check: viewport should still show zero bars.
	body := chartBodyLines(m.View())
	for _, line := range body {
		if strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
			t.Errorf("chart body contains bar block char after RefreshMsg race: %q", line)
			break
		}
	}
}

func TestIntro_RealisticStartupSequence(t *testing.T) {
	// Mimic exactly what cmd/ccpulse/main.go does at startup:
	//   1. WindowSizeMsg (bubbletea-injected)
	//   2. RefreshMsg from line 329 (initial refresh)
	//   3. Possibly more RefreshMsg from watcher
	//   4. IndexProgressMsg Active:true then Active:false from backfill
	//   5. RefreshMsg from backfill completion
	//   6. (eventually) springTickMsg
	// Assert that at no point before the hold tick fires do bar block
	// characters appear in the View(). Then drive ticks and verify the
	// chart eventually settles with full bars.
	m := seedIntroModel(t, false)

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("WindowSizeMsg returned nil cmd; intro did not arm")
	}

	// Production sends several non-tick messages before the 150 ms hold
	// tick fires. Each must NOT kill the intro and the View must
	// continue to render zero-height bars.
	preTickMessages := []tea.Msg{
		RefreshMsg{},
		RefreshMsg{},
		IndexProgressMsg{Active: true, Done: 0, Total: 100},
		IndexProgressMsg{Active: false, Done: 100, Total: 100},
		RefreshMsg{},
	}
	for i, msg := range preTickMessages {
		updated, _ = m.Update(msg)
		m = updated.(Model)
		if !m.springActive {
			t.Fatalf("springActive=false after message %d (%T); intro was killed", i, msg)
		}
		if m.springPhase != springHolding {
			t.Fatalf("springPhase=%d after message %d (%T); want springHolding", m.springPhase, i, msg)
		}
		body := chartBodyLines(m.View())
		for _, line := range body {
			if strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
				t.Errorf("after message %d (%T): chart body shows bar block char during hold: %q", i, msg, line)
				break
			}
		}
	}

	// Now drive the hold tick → grow ticks until settle.
	const maxTicks = 200
	settled := false
	for i := 0; i < maxTicks; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		if !m.springActive {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}

	// Final view should show actual bars now.
	finalBody := chartBodyLines(m.View())
	hasBar := false
	for _, line := range finalBody {
		if strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
			hasBar = true
			break
		}
	}
	if !hasBar {
		t.Errorf("after settle: chart body has no bar block chars; want fully-rendered bars")
	}
}

func TestIntro_EmptyCacheDeferred(t *testing.T) {
	// When the cache starts empty, the first WindowSizeMsg must NOT arm
	// the intro (lastValues stays nil); introPending stays true. The
	// intro fires on the first RefreshMsg that produces non-empty data.
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}

	m := New(Deps{Cache: c})
	if !m.introPending {
		t.Fatalf("introPending = false after New(ReduceMotion=false); want true (sanity)")
	}

	// First WindowSizeMsg with empty cache: no arm, introPending stays.
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("cmd = %v after WindowSizeMsg with empty cache; want nil (deferred)", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true with empty cache; want false")
	}
	if !m.introPending {
		t.Errorf("introPending = false after empty-cache WindowSizeMsg; want true (still pending)")
	}

	// Populate the cache and deliver a RefreshMsg.
	now := time.Now().UTC().Truncate(15 * time.Minute)
	if err := c.InsertMessages([]parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-30 * time.Minute), InputTokens: 10000, OutputTokens: 5000},
	}, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	updated, cmd = m.Update(RefreshMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("cmd = nil after RefreshMsg with non-empty cache; want non-nil hold tick (intro arm)")
	}
	if !m.springActive {
		t.Errorf("springActive = false after RefreshMsg; want true (intro armed)")
	}
	if m.springPhase != springHolding {
		t.Errorf("springPhase = %d after RefreshMsg arm; want springHolding (%d)", m.springPhase, springHolding)
	}
	if m.introPending {
		t.Errorf("introPending = true after intro arm; want false (one-shot)")
	}
}

func TestQuotaIntroRatio_SteadyStateReturnsTarget(t *testing.T) {
	// In steady state (springIntro=false) the helper must return its
	// target argument unchanged for both sides — quotaBars() relies on
	// this so today's render is byte-for-byte preserved when no intro
	// is in flight.
	m := New(Deps{ReduceMotion: false})
	m.springIntro = false

	cases := []struct {
		name   string
		side   quotaSide
		target float64
	}{
		{"5h_zero", quotaSide5h, 0.0},
		{"5h_half", quotaSide5h, 0.5},
		{"5h_full", quotaSide5h, 1.0},
		{"7d_zero", quotaSide7d, 0.0},
		{"7d_half", quotaSide7d, 0.5},
		{"7d_full", quotaSide7d, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := m.quotaIntroRatio(tc.side, tc.target)
			if got != tc.target {
				t.Errorf("quotaIntroRatio(%v, %v) = %v; want %v (steady state passes through)",
					tc.side, tc.target, got, tc.target)
			}
		})
	}
}

func TestBeginIntroAnimation_SeedsQuotaTargets(t *testing.T) {
	// After arming, the quota springs must reflect the window snapshot:
	//   - quotaTarget5h  = Percent / 100
	//   - quotaTarget7d  = Percent7d / 100 when Has7d, else 0
	//   - quotaRatio5h/7d, quotaVel5h/7d = 0 (Phase 2 init seeded in
	//     the springHolding tick, not at arm).
	cases := []struct {
		name      string
		percent5h int
		has7d     bool
		percent7d int
		want5h    float64
		want7d    float64
	}{
		{"both_sides_data", 80, true, 25, 0.80, 0.25},
		{"5h_only", 60, false, 0, 0.60, 0.0},
		{"both_zero", 0, true, 0, 0.0, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := seedIntroModel(t, false)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			m = updated.(Model)

			// Overwrite window to the test scenario, then re-arm by
			// resetting the intro state and calling beginIntroAnimation
			// directly. This isolates the seed logic from the
			// maybeArmIntro gate.
			m.springActive = false
			m.springIntro = false
			m.springPhase = springIdle
			m.window.Percent = tc.percent5h
			m.window.Has7d = tc.has7d
			m.window.Percent7d = tc.percent7d
			m.beginIntroAnimation()

			if got := m.quotaTarget5h; got != tc.want5h {
				t.Errorf("quotaTarget5h = %v; want %v", got, tc.want5h)
			}
			if got := m.quotaTarget7d; got != tc.want7d {
				t.Errorf("quotaTarget7d = %v; want %v", got, tc.want7d)
			}
			if m.quotaRatio5h != 0 || m.quotaRatio7d != 0 {
				t.Errorf("quotaRatio5h/7d = %v/%v; want 0/0 (seeded by springHolding tick, not at arm)",
					m.quotaRatio5h, m.quotaRatio7d)
			}
			if m.quotaVel5h != 0 || m.quotaVel7d != 0 {
				t.Errorf("quotaVel5h/7d = %v/%v; want 0/0 (seeded by springHolding tick, not at arm)",
					m.quotaVel5h, m.quotaVel7d)
			}
		})
	}
}

func TestIntro_QuotaBars_HoldFrameRendersZeroFill(t *testing.T) {
	// During springHolding both quota bar rows must render with no
	// filled-block characters — quotaIntroRatio returns 0 for both
	// sides, so bubbles/progress emits only the empty char ('░').
	m := seedIntroModel(t, false)
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("WindowSizeMsg returned nil cmd; intro did not arm")
	}
	if m.springPhase != springHolding {
		t.Fatalf("springPhase = %d after WindowSizeMsg; want springHolding (%d)",
			m.springPhase, springHolding)
	}

	rows := quotaBarLines(m.View())
	if len(rows) == 0 {
		t.Fatalf("no quota bar rows in rendered view")
	}
	for _, row := range rows {
		if strings.ContainsRune(row, '█') {
			t.Errorf("quota bar row contains '█' during hold frame: %q", row)
		}
	}
}

func TestIntro_QuotaBars_HoldTickSeedsVelocities(t *testing.T) {
	// The hold tick (the springHolding case of springTickMsg) must
	// seed quotaVel5h/7d = phase2InitialVelocityV0 * quotaTarget5h/7d,
	// matching the bucket springs' V_i = V0 * target_i contract.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Override targets to known non-zero values: the seedIntroModel
	// fixture has no Anthropic quota loaded so window.Percent is 0,
	// which would let V0*target = 0 trivially satisfy the assertion.
	// Setting explicit non-zero targets here verifies the springHolding
	// arm actually computes V0*target rather than no-op'ing.
	m.quotaTarget5h = 0.8
	m.quotaTarget7d = 0.25
	t5h := m.quotaTarget5h
	t7d := m.quotaTarget7d

	// Deliver the hold tick. The handler switches springPhase to
	// springGrowing AND seeds Phase 2 state (target ratios + velocities).
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)

	if m.springPhase != springGrowing {
		t.Fatalf("springPhase = %d after hold tick; want springGrowing (%d)",
			m.springPhase, springGrowing)
	}
	wantVel5h := phase2InitialVelocityV0 * t5h
	wantVel7d := phase2InitialVelocityV0 * t7d
	if m.quotaVel5h != wantVel5h {
		t.Errorf("quotaVel5h = %v; want %v (V0 * target = %v * %v)",
			m.quotaVel5h, wantVel5h, phase2InitialVelocityV0, t5h)
	}
	if m.quotaVel7d != wantVel7d {
		t.Errorf("quotaVel7d = %v; want %v (V0 * target = %v * %v)",
			m.quotaVel7d, wantVel7d, phase2InitialVelocityV0, t7d)
	}
}

func TestIntro_QuotaBars_GrowLadderRisesMonotonically(t *testing.T) {
	// During springGrowing the quota ratios must rise monotonically
	// from 0 toward their targets, never overshooting (critical damping
	// guarantees no oscillation).
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Override targets to known non-zero values (seedIntroModel has
	// no Anthropic quota loaded → window.Percent=0). The springHolding
	// tick below reads these to seed velocities; the grow ticks then
	// integrate against them.
	m.quotaTarget5h = 0.8
	m.quotaTarget7d = 0.25

	// Deliver the hold tick to enter springGrowing.
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)
	if m.springPhase != springGrowing {
		t.Fatalf("springPhase = %d after hold tick; want springGrowing", m.springPhase)
	}

	target5h := m.quotaTarget5h
	target7d := m.quotaTarget7d
	if target5h <= 0 {
		t.Fatalf("quotaTarget5h = %v; want > 0 for monotonicity check", target5h)
	}

	prev5h := m.quotaRatio5h
	prev7d := m.quotaRatio7d
	const maxTicks = 200
	sawRise5h := false
	sawRise7d := false
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		if m.quotaRatio5h+1e-9 < prev5h {
			t.Fatalf("tick %d: quotaRatio5h regressed %v → %v", i, prev5h, m.quotaRatio5h)
		}
		if m.quotaRatio7d+1e-9 < prev7d {
			t.Fatalf("tick %d: quotaRatio7d regressed %v → %v", i, prev7d, m.quotaRatio7d)
		}
		if m.quotaRatio5h > target5h+1e-6 {
			t.Fatalf("tick %d: quotaRatio5h %v overshot target %v", i, m.quotaRatio5h, target5h)
		}
		if m.quotaRatio7d > target7d+1e-6 {
			t.Fatalf("tick %d: quotaRatio7d %v overshot target %v", i, m.quotaRatio7d, target7d)
		}
		if m.quotaRatio5h > prev5h {
			sawRise5h = true
		}
		if m.quotaRatio7d > prev7d {
			sawRise7d = true
		}
		prev5h = m.quotaRatio5h
		prev7d = m.quotaRatio7d
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	if !sawRise5h {
		t.Errorf("quotaRatio5h never rose during grow phase")
	}
	if target7d > 0 && !sawRise7d {
		t.Errorf("quotaRatio7d never rose during grow phase (target was %v)", target7d)
	}
}

func TestIntro_QuotaBars_SettleTogetherWithChart(t *testing.T) {
	// On the tick that the chart bucket springs settle (maxGap <
	// phaseTransitionThreshold), the quota springs must also be within
	// threshold of their targets. The shared maxGap fold makes this
	// true by construction; the test guards the contract.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Override targets to non-zero values so settle-within-threshold
	// is a meaningful claim (without it both quota springs settle at 0
	// trivially and the assertion is vacuous).
	m.quotaTarget5h = 0.8
	m.quotaTarget7d = 0.25

	const maxTicks = 300
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	if math.Abs(m.quotaTarget5h-m.quotaRatio5h) > phaseTransitionThreshold {
		t.Errorf("quotaRatio5h = %v after settle, want within %v of target %v",
			m.quotaRatio5h, phaseTransitionThreshold, m.quotaTarget5h)
	}
	if math.Abs(m.quotaTarget7d-m.quotaRatio7d) > phaseTransitionThreshold {
		t.Errorf("quotaRatio7d = %v after settle, want within %v of target %v",
			m.quotaRatio7d, phaseTransitionThreshold, m.quotaTarget7d)
	}
}

func TestIntro_QuotaBars_ReduceMotion_FirstFrameFull(t *testing.T) {
	// With ReduceMotion=true: no intro arms (introPending=false from
	// New()), no springs are seeded, quotaIntroRatio returns target
	// directly, and the first View() renders both bars at their target
	// fill (which contains '█' for non-zero targets).
	m := seedIntroModel(t, true) // reduceMotion=true
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	if cmd != nil {
		t.Errorf("cmd = %v after first WindowSizeMsg with reduce_motion; want nil", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true with reduce_motion; want false")
	}

	// Override window.Percent so the visual probe has a non-zero target
	// to render — seedIntroModel has no Anthropic quota loaded, so the
	// natural Percent is 0 and bubbles/progress would emit only empty
	// chars regardless of the helper's behaviour.
	m.window.Percent = 60
	m.window.Has7d = true
	m.window.Percent7d = 25

	rows := quotaBarLines(m.View())
	if len(rows) == 0 {
		t.Fatalf("no quota bar rows in rendered view")
	}
	// At least one row should contain a filled block character since the
	// override made both targets non-zero and the helper passes through
	// to bubbles/progress under reduce_motion.
	var sawFill bool
	for _, row := range rows {
		if strings.ContainsRune(row, '█') {
			sawFill = true
			break
		}
	}
	if !sawFill {
		t.Errorf("no '█' in quota bar rows with reduce_motion and non-zero Percent — bars should paint at full target on first frame")
	}
}

func TestIntro_QuotaBars_OneShot_NoReArmAfterSettle(t *testing.T) {
	// After the intro settles, a second WindowSizeMsg (or RefreshMsg
	// with a different quota value) must not re-arm the quota intro.
	// introPending is cleared on first arm and never re-set.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	const maxTicks = 300
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	settled5h := m.quotaRatio5h
	settled7d := m.quotaRatio7d

	// Second WindowSizeMsg — must not re-arm.
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("cmd = %v after post-settle WindowSizeMsg; want nil (no re-arm)", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true after post-settle WindowSizeMsg; want false")
	}
	if m.quotaRatio5h != settled5h || m.quotaRatio7d != settled7d {
		t.Errorf("quota ratios moved after post-settle WindowSizeMsg: %v/%v → %v/%v",
			settled5h, settled7d, m.quotaRatio5h, m.quotaRatio7d)
	}
}

func TestIntro_QuotaBars_DeferredArmOnEmptyCache(t *testing.T) {
	// Open with an empty cache: the chart-intro gate
	// (len(lastValues) > 0) blocks arming, so the quota intro defers
	// too (it shares introPending). The first non-empty RefreshMsg
	// arms both surfaces.
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	m := New(Deps{Cache: c, ReduceMotion: false})

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("cmd = %v after WindowSizeMsg with empty cache; want nil (deferred arm)", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true after WindowSizeMsg with empty cache; want false")
	}
	if !m.introPending {
		t.Errorf("introPending = false after WindowSizeMsg with empty cache; want true (still pending)")
	}

	// Insert a message; the next RefreshMsg should arm.
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	if err := c.InsertMessages([]parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp: now.Add(-10 * time.Minute), InputTokens: 30000, OutputTokens: 15000},
	}, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	updated, cmd = m.Update(RefreshMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("cmd = nil after non-empty RefreshMsg; want hold tick (deferred arm should fire)")
	}
	if !m.springActive {
		t.Errorf("springActive = false after non-empty RefreshMsg; want true (intro armed)")
	}
	if m.springPhase != springHolding {
		t.Errorf("springPhase = %d after non-empty RefreshMsg; want springHolding", m.springPhase)
	}
}

func TestIntro_QuotaBars_NoData7d_PlaceholderUnchanged(t *testing.T) {
	// When Has7d=false, the 7d side renders the (no data) placeholder
	// regardless of intro phase. The intro fires for the 5h side only;
	// the 7d spring is seeded with target=0 defensively but unread.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Overwrite the window to drop the 7d side, then re-arm via
	// beginIntroAnimation directly (isolating from maybeArmIntro
	// gating).
	m.springActive = false
	m.springIntro = false
	m.springPhase = springIdle
	m.window.Has7d = false
	m.window.Percent7d = 0
	m.beginIntroAnimation()

	if m.quotaTarget7d != 0 {
		t.Errorf("quotaTarget7d = %v with Has7d=false; want 0 (defensive zero)", m.quotaTarget7d)
	}

	// 5h and 7d sides render on the same row via JoinHorizontal; the
	// 7d side appears as the "(no data)" placeholder regardless of
	// intro phase when Has7d=false.
	view := m.View()
	if !strings.Contains(view, "(no data)") {
		t.Errorf("rendered view missing '(no data)' placeholder with Has7d=false; got rows=%q",
			quotaBarLines(view))
	}
}

// quotaUsage builds an *anthro.Usage suitable for QuotaMsg fixtures.
// Both buckets reset 2h / 48h from now so status.Compute treats them
// as live. Pass utilFiveHour/utilSevenDay as percent values [0, 100].
func quotaUsage(utilFiveHour, utilSevenDay float64) *anthro.Usage {
	now := time.Now()
	return &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: utilFiveHour, ResetsAt: timePtr(now.Add(2 * time.Hour))},
		SevenDay: &anthro.Bucket{Utilization: utilSevenDay, ResetsAt: timePtr(now.Add(48 * time.Hour))},
	}
}

func TestIntro_QuotaBars_QuotaArrivesDuringHold(t *testing.T) {
	// Open the intro with quota nil (the common startup race: the
	// async Anthropic poller hasn't finished yet). beginIntroAnimation
	// snapshots quotaTarget5h/7d as 0 because m.window.Percent is 0.
	// When QuotaMsg arrives DURING the hold beat, the handler must
	// re-snapshot the targets to the real values so the upcoming hold
	// tick seeds the velocities (V0 * target) from real values, and
	// the grow ramps to a visible end-state instead of 0.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if m.springPhase != springHolding {
		t.Fatalf("springPhase = %d after WindowSize; want springHolding", m.springPhase)
	}
	if m.quotaTarget5h != 0 {
		t.Fatalf("quotaTarget5h = %v at arm; want 0 (no quota loaded yet)", m.quotaTarget5h)
	}

	// Quota arrives during the hold beat.
	updated, _ = m.Update(QuotaMsg{Usage: quotaUsage(80, 25), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	wantTarget5h := float64(m.window.Percent) / 100.0
	wantTarget7d := float64(m.window.Percent7d) / 100.0
	if wantTarget5h <= 0 {
		t.Fatalf("test setup: window.Percent still 0 after QuotaMsg; got %d", m.window.Percent)
	}
	if m.quotaTarget5h != wantTarget5h {
		t.Errorf("quotaTarget5h = %v after QuotaMsg in hold; want %v (live window value)",
			m.quotaTarget5h, wantTarget5h)
	}
	if m.quotaTarget7d != wantTarget7d {
		t.Errorf("quotaTarget7d = %v after QuotaMsg in hold; want %v (live window value)",
			m.quotaTarget7d, wantTarget7d)
	}

	// Drive ticks through hold + grow; quota springs should settle
	// at the real targets (within phaseTransitionThreshold).
	const maxTicks = 300
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	if math.Abs(m.quotaTarget5h-m.quotaRatio5h) > phaseTransitionThreshold {
		t.Errorf("quotaRatio5h = %v after settle; want within %v of target %v",
			m.quotaRatio5h, phaseTransitionThreshold, m.quotaTarget5h)
	}
}

func TestIntro_QuotaBars_QuotaArrivesDuringGrow(t *testing.T) {
	// Similar to the during-hold case, but QuotaMsg arrives AFTER the
	// hold tick has already seeded velocities from the (then-zero)
	// targets. Re-snapshotting the target shifts the spring's
	// destination mid-flight; critical damping ensures the spring
	// eases to the new target without overshoot.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Hold tick → springGrowing.
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)
	if m.springPhase != springGrowing {
		t.Fatalf("springPhase = %d after hold tick; want springGrowing", m.springPhase)
	}
	if m.quotaVel5h != 0 {
		t.Fatalf("quotaVel5h = %v; want 0 (V0 * 0 = 0 because no quota loaded at arm)", m.quotaVel5h)
	}

	// A few grow ticks happen before quota arrives. With targets at 0
	// the springs are stationary at 0 — no visible motion.
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.quotaRatio5h != 0 {
		t.Fatalf("quotaRatio5h = %v after grow ticks with target=0; want 0", m.quotaRatio5h)
	}

	// Quota arrives mid-grow.
	updated, _ = m.Update(QuotaMsg{Usage: quotaUsage(80, 25), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	wantTarget5h := float64(m.window.Percent) / 100.0
	if m.quotaTarget5h != wantTarget5h {
		t.Errorf("quotaTarget5h = %v after mid-grow QuotaMsg; want %v",
			m.quotaTarget5h, wantTarget5h)
	}

	// Drive remaining ticks; quota springs should settle at real targets
	// even though they were seeded with 0 velocities.
	const maxTicks = 300
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("intro did not settle within %d ticks", maxTicks)
	}
	if math.Abs(m.quotaTarget5h-m.quotaRatio5h) > phaseTransitionThreshold {
		t.Errorf("quotaRatio5h = %v after settle; want within %v of target %v",
			m.quotaRatio5h, phaseTransitionThreshold, m.quotaTarget5h)
	}
}

func TestIntro_QuotaBars_QuotaArrivesAfterSettle(t *testing.T) {
	// Slow-network case: chart intro armed and settled with quota=nil
	// (springs animated 0 → 0 invisibly), then QuotaMsg arrives. The
	// QuotaMsg handler must kick a quota-only slide-in so the bars
	// don't snap from 0 to their real values. Skips the hold beat
	// (bars already sat at 0 for the chart intro).
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Drive the chart intro to settle.
	const settleTicks = 300
	for i := 0; i < settleTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("chart intro did not settle within %d ticks", settleTicks)
	}
	if !m.quotaIntroPending {
		t.Fatalf("quotaIntroPending = false after chart intro settled with quota=nil; want true (quota animation still owed)")
	}

	// Quota arrives. The handler should kick a quota-only intro and
	// return a tick.
	updated, cmd := m.Update(QuotaMsg{Usage: quotaUsage(80, 25), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("cmd = nil after late QuotaMsg; want non-nil tick (quota intro should kick)")
	}
	if !m.springActive {
		t.Errorf("springActive = false after late QuotaMsg; want true (quota intro armed)")
	}
	if !m.springIntro {
		t.Errorf("springIntro = false after late QuotaMsg; want true")
	}
	if m.springPhase != springGrowing {
		t.Errorf("springPhase = %d after late QuotaMsg; want springGrowing (skip hold beat — bars already at 0)",
			m.springPhase)
	}
	if m.quotaIntroPending {
		t.Errorf("quotaIntroPending = true after late-arrival kick; want false (now firing)")
	}
	wantTarget5h := float64(m.window.Percent) / 100.0
	if m.quotaTarget5h != wantTarget5h {
		t.Errorf("quotaTarget5h = %v; want %v after late kick", m.quotaTarget5h, wantTarget5h)
	}
	wantVel5h := phase2InitialVelocityV0 * wantTarget5h
	if m.quotaVel5h != wantVel5h {
		t.Errorf("quotaVel5h = %v; want %v (V0 * target — late kick seeds velocity directly, no hold beat)",
			m.quotaVel5h, wantVel5h)
	}
	if m.quotaRatio5h != 0 {
		t.Errorf("quotaRatio5h = %v; want 0 (animation starts from zero)", m.quotaRatio5h)
	}

	// Drive ticks to settle the late-arrival quota intro.
	const maxTicks = 300
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("late quota intro did not settle within %d ticks", maxTicks)
	}
	if math.Abs(m.quotaTarget5h-m.quotaRatio5h) > phaseTransitionThreshold {
		t.Errorf("quotaRatio5h = %v after late settle; want within %v of target %v",
			m.quotaRatio5h, phaseTransitionThreshold, m.quotaTarget5h)
	}
}

func TestIntro_QuotaBars_LateArrival_NoFireAfterFirst(t *testing.T) {
	// After the late-arrival quota intro fires once and settles, a
	// subsequent QuotaMsg (poller fires every 3 minutes) must NOT
	// re-trigger the intro — quotaIntroPending was cleared on first
	// fire.
	m := seedIntroModel(t, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	// Settle chart intro (quota=nil throughout).
	for i := 0; i < 300 && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}

	// First QuotaMsg → late-arrival kick.
	updated, _ = m.Update(QuotaMsg{Usage: quotaUsage(80, 25), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	for i := 0; i < 300 && m.springActive; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}

	// Second QuotaMsg → must NOT re-fire.
	updated, cmd := m.Update(QuotaMsg{Usage: quotaUsage(82, 26), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("cmd = %v after second QuotaMsg post-settle; want nil (no re-fire)", cmd)
	}
	if m.springActive {
		t.Errorf("springActive = true after second QuotaMsg; want false")
	}
}

func TestIntro_QuotaBars_QuotaLoadedAtArm_ClearsPending(t *testing.T) {
	// When quota is already loaded at the moment beginIntroAnimation
	// runs (QuotaMsg arrived BEFORE WindowSize), quotaIntroPending must
	// be cleared so a subsequent post-settle QuotaMsg doesn't trigger
	// the late-arrival path.
	m := seedIntroModel(t, false)

	// Quota loads BEFORE WindowSize.
	updated, _ := m.Update(QuotaMsg{Usage: quotaUsage(80, 25), Source: "api", UpdatedAt: time.Now()})
	m = updated.(Model)
	if !m.quotaIntroPending {
		t.Fatalf("quotaIntroPending = false after QuotaMsg before WindowSize; want true (intro hasn't armed yet)")
	}

	// WindowSize arms the intro with quota loaded.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if m.quotaIntroPending {
		t.Errorf("quotaIntroPending = true after WindowSize with quota loaded; want false (intro armed with real targets, no late path needed)")
	}
	if m.quotaTarget5h == 0 {
		t.Errorf("quotaTarget5h = 0 at arm with quota loaded; expected non-zero target")
	}
}

// TestInitialPaint_PeakFromRightEdgeVisible pins the unified visible-
// slice peak policy on initial paint (#230). The chart pins to the
// right edge on first paint (refreshChart's !hadAnchor branch), so
// the initial visible slice is the most-recent ~visibleBuckets()
// worth of buckets — and m.peak must reflect that slice, not the
// max across all buckets including off-screen outliers.
//
// Seeds 100 old buckets at 9999 input tokens (the outlier — far off
// the right edge once 100 more recent buckets are added) and 100 new
// buckets at 1000 input tokens (the visible slice). After refreshChart,
// m.peak must reflect ~1000, not 9999.
func TestInitialPaint_PeakFromRightEdgeVisible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	// 100 OLDER buckets (offset 100..199 in chronological order) with
	// outlier value 9999 — these will land at the LEFT of the canvas.
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID:   "old",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-(200+i)*15) * time.Minute),
			InputTokens: 9999,
		})
	}
	// 200 RECENT buckets at 1000 — these land at the RIGHT of the canvas
	// (where the initial !hadAnchor branch pins viewportXOffset). Sized
	// so the right-edge visible slice (visibleBuckets() ~ 118 at the
	// test's m.w=120, 15m zoom) fits entirely within the recent run.
	for i := range 200 {
		msgs = append(msgs, parse.Message{
			SessionID:   "new",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: 1000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	// Right-edge visible slice contains only 1000-token buckets; outliers
	// at the canvas's left edge are NOT visible. Peak must reflect that.
	if m.peak > 8000 {
		t.Errorf("m.peak = %v, want ~1000 (right-edge visible slice). "+
			"Outlier (9999) at canvas left edge leaked into peak — the "+
			"unified visible-slice policy is broken.", m.peak)
	}
	if m.peak < 999 {
		t.Errorf("m.peak = %v, want ~1000 (right-edge visible slice). "+
			"Peak appears not to track visible bars at all.", m.peak)
	}
}

// seedOffscreenOutlier seeds 100 old buckets at 9999 input tokens (which
// land OFF-SCREEN to the left at the test's m.w) plus 200 recent buckets
// at 1000 (on-screen at the right edge). Returns a refreshed token-mode
// model whose visible-slice peak is ~1000 while the global max is 9999 —
// the exact shape that makes the spring's per-bar targets blow past 1.0
// if they are normalised by the visible peak instead of being clamped.
func seedOffscreenOutlier(t *testing.T) *Model {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID: "old", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-(200+i)*15) * time.Minute),
			InputTokens: 9999,
		})
	}
	for i := range 200 {
		msgs = append(msgs, parse.Message{
			SessionID: "new", ProjectSlug: "p", Model: "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: 1000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()
	return &m
}

func maxOf(xs []float64) float64 {
	var mx float64
	for _, x := range xs {
		mx = max(mx, x)
	}
	return mx
}

// TestIntroAnimation_OffscreenOutlierTargetsClamped pins that the open-path
// intro spring never produces a per-bar target above 1.0, even when an
// off-screen bucket is far taller than the visible-slice peak (#230). The
// spring renders ratios against a fixed max of 1.0, so any target > 1.0 is
// visually clipped — but it still gates the maxGap settle check, dragging
// the animation out (~550ms → ~2s) until the inflated off-screen springs
// converge to within 0.01 of e.g. 9.99. That long window is what lets a
// startup resize or watcher refresh land mid-animation and rebuild the
// chart at a different peak, producing the "animate then shrink" jump.
func TestIntroAnimation_OffscreenOutlierTargetsClamped(t *testing.T) {
	t.Parallel()
	m := seedOffscreenOutlier(t)

	if m.peak > 8000 {
		t.Fatalf("baseline: m.peak = %v, want ~1000 (visible-slice peak)", m.peak)
	}

	m.beginIntroAnimation()
	if !m.springActive {
		t.Fatalf("intro did not arm (springActive=false)")
	}
	if mx := maxOf(m.springFinalTargets); mx > 1.0+1e-9 {
		t.Errorf("max(springFinalTargets) = %v, want <= 1.0. Off-screen "+
			"bucket (9999) normalised by the visible-slice ceiling produced "+
			"a target far above 1.0, which clips visually but stretches the "+
			"spring settle window (#230 spring-settle jump).", mx)
	}
}

// TestUnitAnimation_OffscreenOutlierTargetsClamped is the unit-toggle
// sibling of the intro test above: switching INTO a bar chart while an
// off-screen bucket dwarfs the visible peak must not yield targets > 1.0.
func TestUnitAnimation_OffscreenOutlierTargetsClamped(t *testing.T) {
	t.Parallel()
	m := seedOffscreenOutlier(t)

	// Re-render in cost mode (the OLD unit) so beginUnitAnimation snapshots
	// cost as old state, then toggle forward into tokens (a bar chart).
	m.unitIdx = int(chartUnitCost)
	m.refreshChart()
	m.unitIdx = int(chartUnitTokens)
	m.beginUnitAnimation()

	if !m.springActive {
		t.Fatalf("unit toggle did not arm (springActive=false)")
	}
	if mx := maxOf(m.springFinalTargets); mx > 1.0+1e-9 {
		t.Errorf("max(springFinalTargets) = %v, want <= 1.0 after toggle into "+
			"tokens. Off-screen outlier normalised by the visible peak "+
			"inflated the target past 1.0 (#230 spring-settle jump).", mx)
	}
}

// TestRefresh_PeakFromVisibleSlice pins that watcher-triggered refresh
// also computes peak from the current visible slice — not the full
// history — when the user has scrolled back to an older window (#230).
//
// Setup mirrors TestInitialPaint_PeakFromRightEdgeVisible but the
// model is then scrolled LEFT to land the viewport over the outlier
// region. After refreshChart, m.peak must reflect the (now-visible)
// outliers, ~9999, not the all-time peak (also 9999, so we additionally
// check the inverse — scroll RIGHT off the outliers and confirm peak
// drops to ~1000).
func TestRefresh_PeakFromVisibleSlice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	// 100 OLDER buckets with outlier 9999.
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID:   "old",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-(200+i)*15) * time.Minute),
			InputTokens: 9999,
		})
	}
	// 100 RECENT buckets at 1000.
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID:   "new",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: 1000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	// After first refresh: peak reflects right-edge slice (~1000).
	if m.peak > 8000 {
		t.Fatalf("baseline: m.peak = %v after initial refresh, want ~1000", m.peak)
	}

	// Scroll LEFT past the recent slice and into the outlier slice.
	// 100 recent buckets at zoom-stride=1 → scroll left by ~150 columns
	// lands the viewport firmly over the outlier region (offsets 50–150).
	for range 150 {
		m.scrollLeft(horizontalScrollStep)
	}

	// Fire a synthetic refresh — exactly what the watcher's RefreshMsg
	// triggers. Peak must now reflect the outliers under the viewport.
	m.refreshChart()
	if m.peak < 9000 {
		t.Errorf("after scroll-left + refresh: m.peak = %v, want ~9999. "+
			"Refresh did not recompute peak from current visible slice — "+
			"the unified visible-slice policy is broken on the refresh path.", m.peak)
	}
}

// TestRenderWindow_ComputesFromVisibleSlice unit-tests the windowed
// steady-state bar render (#255). Seeds the chart via refreshChart, shifts
// viewportXOffset over the outlier region, and asserts renderWindow
// recomputes m.peak from the new visible slice (DB-free) — the same
// contract the deleted rebuildAtVisiblePeak held, now at ~viewport width.
func TestRenderWindow_ComputesFromVisibleSlice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID:   "old",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-(200+i)*15) * time.Minute),
			InputTokens: 9999,
		})
	}
	for i := range 200 {
		msgs = append(msgs, parse.Message{
			SessionID:   "new",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: 1000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	rightEdgePeak := m.peak

	// Move the viewport over the outlier region (offsets 0-99 are the 9999
	// buckets) without a DB round-trip, then re-render the window.
	m.viewport.SetXOffset(0)
	m.viewportXOffset = 0
	m.renderWindow()

	if m.peak < 9000 {
		t.Errorf("renderWindow: m.peak = %v, want ~9999 "+
			"(visible slice now covers outliers at offsets 0-99)", m.peak)
	}
	if m.peak == rightEdgePeak {
		t.Errorf("renderWindow: m.peak = %v unchanged from right-edge baseline %v — "+
			"windowed render appears to be a no-op", m.peak, rightEdgePeak)
	}
}

// TestScrollDuringSpring_PeakUnchanged pins that scrolling while a spring is
// in flight does NOT recompute m.peak (#255 — ports the old #230 rescaleMsg
// spring gate). The spring owns m.peak as its bar-height normalization base
// (renderSpringFrame uses niceCeilingFloat(m.peak)); a live rescale mid-spring
// would shift bar heights under the animation. The viewportXOffset shadow
// still advances so the post-settle refreshChart picks up the new position.
func TestScrollDuringSpring_PeakUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	for i := range 100 {
		msgs = append(msgs, parse.Message{
			SessionID:   "old",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-(200+i)*15) * time.Minute),
			InputTokens: 9999,
		})
	}
	for i := range 200 {
		msgs = append(msgs, parse.Message{
			SessionID:   "new",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: 1000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	peakBeforeSpring := m.peak // right-edge slice (~1000)
	offsetBefore := m.viewportXOffset

	// Spring in flight: scrolling into the outlier region must NOT move peak.
	m.springActive = true
	for range 150 {
		m.scrollLeft(horizontalScrollStep)
	}

	if m.peak != peakBeforeSpring {
		t.Errorf("scroll during spring mutated m.peak (%v → %v); the spring "+
			"must own peak — renderWindow must be gated on !springActive",
			peakBeforeSpring, m.peak)
	}
	if m.viewportXOffset >= offsetBefore {
		t.Errorf("scroll during spring did not advance the viewportXOffset shadow "+
			"(%d → %d); setX must still run so the post-settle refresh picks up "+
			"the new scroll position", offsetBefore, m.viewportXOffset)
	}
}

// TestScroll_RebuildUsesWindowedWidth is the #255 perf-regression guard:
// after a bar-mode scroll the buildChart rebuild must run at ~viewport
// width, NOT the full canvas (m.lastCanvasW). Captures the tui.buildChart
// slog chartW attribute — the same attribute the real-binary debug.log
// check reads. Not parallel: captureLogs swaps the process-global slog
// default.
func TestScroll_RebuildUsesWindowedWidth(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	for i := range 400 {
		msgs = append(msgs, parse.Message{
			SessionID:   "s",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: int64(1000 + i),
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	if m.lastCanvasW <= m.viewport.Width {
		t.Fatalf("setup: lastCanvasW=%d must exceed viewport.Width=%d for the "+
			"windowing assertion to be meaningful", m.lastCanvasW, m.viewport.Width)
	}

	recs := captureLogs(t, slog.LevelDebug)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)

	var chartW int64 = -1
	for _, r := range recs() {
		if r.Message != "tui.buildChart" {
			continue
		}
		if v, ok := attrMap(r)["chartW"].(int64); ok {
			chartW = v
		}
	}
	if chartW < 0 {
		t.Fatalf("no tui.buildChart record captured after scroll; the windowed "+
			"rebuild did not run")
	}
	if chartW >= int64(m.lastCanvasW) {
		t.Errorf("scroll rebuild chartW=%d, want < lastCanvasW=%d — the windowed "+
			"render must not rebuild the full canvas (#255)", chartW, m.lastCanvasW)
	}
	if maxWant := int64(m.viewport.Width + ZoomLevels[m.zoomIdx].stride()); chartW > maxWant {
		t.Errorf("scroll rebuild chartW=%d exceeds viewport width + one bucket (%d); "+
			"the window is wider than expected", chartW, maxWant)
	}
}

// TestRefresh_RestoresRightEdgeAfterNarrowContent guards that after
// refreshChart, the chart stays pinned to the right edge
// (HorizontalScrollPercent == 1.0 / current time) when the viewport
// holds narrow spring-animation content. Under #255, refreshChart calls
// renderWindow (for bar modes), which rebuilds windowed content and sets
// the viewport offset in a single operation via SetXOffset — no separate
// re-apply setX step against stale narrow content. The test verifies the
// right-edge observable is preserved through this windowed renderWindow
// path. The earlier #230 tests only checked m.peak, which is derived from
// the shadow offset; this probes the REAL viewport offset via
// HorizontalScrollPercent.
func TestRefresh_RestoresRightEdgeAfterNarrowContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)

	var msgs []parse.Message
	for i := range 300 {
		msgs = append(msgs, parse.Message{
			SessionID:   "s",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens: int64(1000 + i),
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()

	// Simulate the spring's last frame: it leaves NARROW content (only the
	// visible window) in the viewport with a small xOffset, while the
	// shadow m.viewportXOffset stays pinned to the right edge. This bypass
	// of setX mirrors renderSpringFrame, which legitimately drives the real
	// viewport offset for animation frames without touching the shadow.
	narrowLine := strings.Repeat("x", m.viewport.Width)
	narrow := make([]string, m.viewport.Height)
	for i := range narrow {
		narrow[i] = narrowLine
	}
	m.viewport.SetContent(strings.Join(narrow, "\n"))
	m.viewport.SetXOffset(0)

	// Spring settles → refreshChart fires with the shadow still pinned right.
	m.refreshChart()

	if got := m.viewport.HorizontalScrollPercent(); got < 0.999 {
		t.Errorf("HorizontalScrollPercent = %v after refresh following "+
			"narrow spring content; want 1.0 (right edge / current time). "+
			"The real viewport offset was clamped against stale narrow "+
			"content, so the chart jumped to the earliest bucket "+
			"(#230 spring-settle regression).", got)
	}
}
