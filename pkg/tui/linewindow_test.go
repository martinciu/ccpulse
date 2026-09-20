package tui

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/martinciu/ccpulse/pkg/cache"
)

var lineWindowNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// lineChartBuildWidths returns the chartW attribute of every
// "tui.buildLineChart" debug record captured so far.
func lineChartBuildWidths(recs []slog.Record) []int64 {
	var out []int64
	for _, r := range recs {
		if r.Message != "tui.buildLineChart" {
			continue
		}
		if w, ok := attrMap(r)["chartW"].(int64); ok {
			out = append(out, w)
		}
	}
	return out
}

// TestRefreshChart_Remaining_BuildsAtViewportWidth is the headline guard for
// #528: the steady-state usage line chart must be built at the viewport's
// width, never at the logical canvas width. Observed through buildLineChart's
// own debug record, because a bubbles v1 viewport exposes no content getter and
// View() is already cut to width — so a painted-frame assertion cannot tell a
// 20,000-column canvas from a 116-column one.
func TestRefreshChart_Remaining_BuildsAtViewportWidth(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	if m.lastCanvasW <= m.viewport.Width {
		t.Fatalf("precondition: logical canvas %d must exceed viewport %d", m.lastCanvasW, m.viewport.Width)
	}

	recs := captureLogs(t)
	m.refreshChart()

	widths := lineChartBuildWidths(recs())
	if len(widths) == 0 {
		t.Fatal("no tui.buildLineChart record captured — this test observes nothing")
	}
	for _, w := range widths {
		if w != int64(m.viewport.Width) {
			t.Errorf("buildLineChart chartW = %d, want viewport width %d (logical canvas is %d)",
				w, m.viewport.Width, m.lastCanvasW)
		}
	}
}

// TestApplyBreakdownResize_Remaining_BuildsAtViewportWidth: the breakdown-panel
// reflow is a steady-state paint too — it runs on a debounced scroll-settle and
// on every quota poll that changes the header's row count — so it must window
// the line chart exactly as refreshChart does, not rebuild the logical canvas.
func TestApplyBreakdownResize_Remaining_BuildsAtViewportWidth(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	if m.lastCanvasW <= m.viewport.Width {
		t.Fatalf("precondition: logical canvas %d must exceed viewport %d", m.lastCanvasW, m.viewport.Width)
	}
	m.viewport.Height = m.chartHeight() - 1 // stale height: forces the reflow to repaint

	recs := captureLogs(t)
	m.applyBreakdownResize()

	widths := lineChartBuildWidths(recs())
	if len(widths) != 1 || widths[0] != int64(m.viewport.Width) {
		t.Errorf("buildLineChart widths = %v, want exactly one build at viewport width %d (logical canvas is %d)",
			widths, m.viewport.Width, m.lastCanvasW)
	}
	if m.viewport.Height != m.chartHeight() {
		t.Errorf("viewport.Height = %d, want %d — the reflow did not re-sync the height", m.viewport.Height, m.chartHeight())
	}
}

// TestRenderWindow_Remaining_ZeroSamplesStillPaints: remaining mode with a long
// message history but no usage samples has empty lastValues. renderWindow's
// bar-mode guard (len(lastValues) == 0 → return) must not swallow the line
// render — the flat 100% baseline still has to be painted, windowed.
func TestRenderWindow_Remaining_ZeroSamplesStillPaints(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 0, lineWindowNow)
	defer c.Close()
	if len(m.lastValues) != 0 {
		t.Fatalf("precondition: want no usage samples, got %d values", len(m.lastValues))
	}

	recs := captureLogs(t)
	m.renderWindow()

	widths := lineChartBuildWidths(recs())
	if len(widths) != 1 || widths[0] != int64(m.viewport.Width) {
		t.Errorf("buildLineChart widths = %v, want exactly one build at viewport width %d", widths, m.viewport.Width)
	}
	if strings.TrimSpace(ansi.Strip(m.viewport.View())) == "" {
		t.Error("viewport is blank — the zero-samples baseline was not painted")
	}
}

// TestRenderLineWindow_LabelRowMatchesFullCanvasCut pins the label row of the
// windowed frame to the full-canvas row cut at the CLAMPED column offset.
//
// The 24h/pinned-right case is the one that bites: setX ceil-divides maxX, so
// viewportXOffset*stride can overshoot the canvas right edge by up to stride-1
// (11) columns (#206). Cutting the label row at the unclamped offset slides the
// labels off the plot by that much.
func TestRenderLineWindow_LabelRowMatchesFullCanvasCut(t *testing.T) {
	withForcedColor(t)

	for zi, z := range ZoomLevels {
		for _, pos := range []string{"pinned-right", "middle", "leftmost"} {
			t.Run(z.Label+"/"+pos, func(t *testing.T) {
				// 4,000 15m-buckets ≈ 41 days: wide at every zoom (24h → ~490 cols).
				m, c := seedWideRemainingModel(t, 4_000, 400, lineWindowNow)
				defer c.Close()
				m.zoomIdx = zi
				m.refreshChart()
				if m.lastCanvasW <= m.viewport.Width {
					t.Fatalf("precondition: canvas %d must exceed viewport %d", m.lastCanvasW, m.viewport.Width)
				}
				switch pos {
				case "middle":
					m.scrollLeft(m.viewportXOffset / 2)
				case "leftmost":
					m.scrollLeft(m.viewportXOffset)
				}

				full := renderXLabels(synthLabelStarts(m.lastChartFrom, m.lastChartTo, z),
					m.lastCanvasW, z, m.now(), m.dateOrder)
				xOff := m.visibleXOffset(m.lastCanvasW)
				want := ansi.Cut(full, xOff, xOff+m.viewport.Width)

				if !strings.Contains(m.viewport.View(), want) {
					t.Errorf("label row is not the full-canvas row cut at the clamped offset %d\nwant: %q\ngot view:\n%s",
						xOff, want, m.viewport.View())
				}
			})
		}
	}
}

// TestRenderLineWindow_ShortChartNoLabelRow: below chartH 6 buildLineChart
// emits no label row. The windowed render must neither panic nor fall back to a
// wide build.
func TestRenderLineWindow_ShortChartNoLabelRow(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	m.h = 12 // squeeze the terminal so the chart band drops under 6 rows
	m.viewport.Height = m.chartHeight()
	if m.chartHeight() >= 6 {
		t.Fatalf("precondition: chartHeight = %d, want < 6", m.chartHeight())
	}

	recs := captureLogs(t)
	m.refreshChart()

	for _, w := range lineChartBuildWidths(recs()) {
		if w != int64(m.viewport.Width) {
			t.Errorf("buildLineChart chartW = %d, want viewport width %d", w, m.viewport.Width)
		}
	}
}

// TestBuildLineChart_OutOfWindowPointsDoNotWidenTheTimeRange pins the root
// cause of the line-mode scroll shimmer that windowing the steady state exposed
// (#528). timeserieslinechart.New turns auto-ranging on unconditionally and
// WithTimeRange does not turn it off, so every pushed point outside [from, to]
// silently WIDENS the rendered time range. slicePointsInRange pads one such
// point per side on purpose (edge continuity), so a windowed frame used to show
// slightly more than its window — by an amount that depends on where the window
// sits relative to the samples. That made the x-scale pulse while scrolling and
// slid the plot off the label row, which assumes exactly one bucket per column.
//
// Two renders of the same window: one with a spike only, one with the spike
// plus an out-of-window point per side. The pads sit at 100% headroom, so the
// only ink they may add is along the top row (where the 7d baseline already
// is). Every other row must be identical — the spike may not move.
func TestBuildLineChart_OutOfWindowPointsDoNotWidenTheTimeRange(t *testing.T) {
	withForcedColor(t)
	const vpW = 120
	zoom := ZoomLevels[0] // 15m: one column is exactly one bucket
	from := lineWindowNow.Add(-vpW * zoom.Duration)
	to := lineWindowNow
	col := func(c int) time.Time { return from.Add(time.Duration(c) * zoom.Duration) }

	spike := []cache.UtilizationPoint{
		{At: col(40), Pct: 0}, {At: col(41), Pct: 100}, {At: col(42), Pct: 0},
	}
	padded := append([]cache.UtilizationPoint{{At: col(-2), Pct: 0}}, spike...)
	padded = append(padded, cache.UtilizationPoint{At: col(vpW + 2), Pct: 0})

	render := func(pts []cache.UtilizationPoint) []string {
		body := buildLineChart(pts, nil, from, to, vpW, 33, lineWindowNow, zoom, dateOrderDayFirst, "test", "")
		return strings.Split(ansi.Strip(body), "\n")[1:] // drop the top row: the pads legitimately ink it
	}
	want, got := render(spike), render(padded)
	if len(want) != len(got) {
		t.Fatalf("row count: spike-only=%d padded=%d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("row %d differs — out-of-window points moved the in-window spike, so they widened the time range\nspike only: %q\nwith pads:  %q",
				i+1, want[i], got[i])
		}
	}
}

// plotRows returns the ANSI-stripped plot rows of the current viewport content
// (the x-label row, last, is dropped).
func plotRows(m *Model) []string {
	rows := strings.Split(ansi.Strip(m.viewport.View()), "\n")
	if len(rows) > 1 {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// TestScroll_Remaining_IsPureTranslation guards the one quality the old
// full-canvas scroll had for free: moving the viewport never redrew the line,
// so it could not shimmer. With a windowed re-render per keypress, a one-bucket
// scroll must still produce the previous plot shifted by exactly one column.
// 15m zoom: one column is exactly 900 s, so the time→column mapping is
// translation-invariant by construction and any mismatch is a real defect.
// Two columns at each edge are excluded — that is where the padded
// out-of-window anchor points enter and leave.
func TestScroll_Remaining_IsPureTranslation(t *testing.T) {
	withForcedColor(t)
	m, c := seedWideRemainingModel(t, 600, 300, lineWindowNow)
	defer c.Close()
	m.scrollLeft(40) // interior: away from both canvas edges

	before := plotRows(&m)
	m.scrollLeft(1) // window moves one column earlier → content shifts right by one
	after := plotRows(&m)

	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("row count: before=%d after=%d", len(before), len(after))
	}
	const edge = 2
	for i := range before {
		b, a := []rune(before[i]), []rune(after[i])
		if len(b) != len(a) {
			t.Fatalf("row %d width: before=%d after=%d", i, len(b), len(a))
		}
		for k := edge + 1; k < len(a)-edge; k++ {
			if a[k] != b[k-1] {
				t.Fatalf("row %d col %d: after=%q, want before[%d]=%q — scrolling redrew the line instead of translating it\nbefore: %s\nafter:  %s",
					i, k, a[k], k-1, b[k-1], before[i], after[i])
			}
		}
	}
}

// TestScroll_Remaining_RerendersAtViewportWidth: a scroll keypress in line mode
// now repaints (renderWindow → renderLineWindow), and does so at viewport width.
func TestScroll_Remaining_RerendersAtViewportWidth(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()

	recs := captureLogs(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)

	widths := lineChartBuildWidths(recs())
	if len(widths) == 0 {
		t.Fatal("scroll keypress did not re-render the line chart")
	}
	for _, w := range widths {
		if w != int64(m.viewport.Width) {
			t.Errorf("buildLineChart chartW = %d, want viewport width %d", w, m.viewport.Width)
		}
	}
}

// TestScroll_Remaining_DuringSpringNeverBlanks: during a u-toggle spring a
// scroll keypress only advances the logical offset (setX) — no render — and
// setX applies a PHYSICAL offset of n×stride. Against the old wide content that
// was meaningful; against viewport-wide content bubbles clamps it to 0
// (SetXOffset clamps to longestLineWidth-Width). This pins that the frame is
// never blank between the keypress and the next tick.
func TestScroll_Remaining_DuringSpringNeverBlanks(t *testing.T) {
	withForcedColor(t)
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	m.scrollLeft(40)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Skip("u-toggle did not arm a spring (reduce-motion?) — nothing to guard")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)

	if strings.TrimSpace(ansi.Strip(m.viewport.View())) == "" {
		t.Error("viewport blanked after a scroll keypress during the spring")
	}
}
