package tui

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/martinciu/ccpulse/pkg/anthro"
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

// TestRenderBreakdownFrame_Remaining_BuildsAtViewportWidth closes the slide leg
// of the "no line build uses the logical canvas" rule. The breakdown slide has
// its own render entry point, and endpoint-identity alone cannot see a
// regression here: a full-canvas build plus a physical offset cuts to the same
// visible frame, so the slide would still settle correctly while paying the
// O(canvas) cost #528 removed.
func TestRenderBreakdownFrame_Remaining_BuildsAtViewportWidth(t *testing.T) {
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	if m.lastCanvasW <= m.viewport.Width {
		t.Fatalf("precondition: logical canvas %d must exceed viewport %d", m.lastCanvasW, m.viewport.Width)
	}

	recs := captureLogs(t)
	m.renderBreakdownFrame()

	widths := lineChartBuildWidths(recs())
	if len(widths) != 1 || widths[0] != int64(m.viewport.Width) {
		t.Errorf("buildLineChart widths = %v, want exactly one build at viewport width %d (logical canvas is %d)",
			widths, m.viewport.Width, m.lastCanvasW)
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

				// Assert the clamp itself, not just the cut. `want` below is built
				// with visibleXOffset — the same call renderLineWindow makes — so
				// expectation and actual move together and a regression INSIDE the
				// helper is invisible to the comparison. This line is what sees it:
				// past this bound the plot window starts later than the frame it is
				// painted into, and the labels sit that many columns off (#206/D4).
				if maxOff := m.lastCanvasW - m.viewport.Width; xOff > maxOff {
					t.Errorf("visibleXOffset = %d, want <= %d — the window runs past the canvas right edge",
						xOff, maxOff)
				}
				// Pinned right, the offset is that bound exactly: the right edge is
				// flush with "now". Stated as a constant so it holds independently
				// of how visibleXOffset computes it.
				if pos == "pinned-right" {
					if want := m.lastCanvasW - m.viewport.Width; xOff != want {
						t.Errorf("pinned-right visibleXOffset = %d, want %d", xOff, want)
					}
				}

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

	widths := lineChartBuildWidths(recs())
	if len(widths) == 0 {
		t.Fatal("no tui.buildLineChart record captured — this test observes nothing (a guard that skips painting a short chart would slip past it)")
	}
	for _, w := range widths {
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
// so it could not shimmer. With a windowed re-render per keypress, one bucket
// of scroll must still produce the previous plot shifted by exactly stride
// columns.
//
// It SWEEPS, deliberately. A single scroll from one position lands on a clean
// offset about half the time, so a one-step version of this test reads green
// while every other keypress of a real drag redraws the line. Each zoom below
// is swept across many consecutive keypresses and every one of them must
// translate.
//
// Two columns at each edge are excluded — that is where the padded
// out-of-window anchor points enter and leave.
func TestScroll_Remaining_IsPureTranslation(t *testing.T) {
	withForcedColor(t)

	// All three zooms. 24h matters most of the three: it is the only one with
	// stride > 1 (12 columns per bucket), so it is the only case that exercises
	// the k-stride comparison at all — an off-by-stride error in the windowed
	// render is invisible to 15m and 1h, which both have stride 1. It needs a
	// longer fixture to have room to scroll: 2,000 buckets is only ~11 scroll
	// positions at stride 12, which trips the clamp guard below.
	for _, zi := range []int{0, 1, 2} {
		zoom := ZoomLevels[zi]
		buckets := 2_000
		if zoom.stride() > 1 {
			buckets = 12_000
		}
		// Two sample densities. Neither divides the 15m lattice evenly (spacing
		// is 6,000 s and ~6,498 s against 900 s), but 300 re-aligns every third
		// sample where 277 near-never does, so a pass cannot come from one
		// convenient alignment.
		for _, nSamples := range []int{300, 277} {
			t.Run(fmt.Sprintf("%s/samples=%d", zoom.Label, nSamples), func(t *testing.T) {
				m, c := seedWideRemainingModel(t, buckets, nSamples, lineWindowNow)
				defer c.Close()
				m.zoomIdx = zi
				m.refreshChart()
				if len(m.lastPts5h) == 0 {
					t.Fatal("precondition: fixture has no usage samples — flat baselines translate trivially and this sweep would pass on nothing")
				}
				m.scrollLeft(10) // interior: away from both canvas edges

				const edge, steps = 2, 60
				stride := zoom.stride()
				before := plotRows(&m)
				for step := range steps {
					prevOff := m.viewportXOffset
					m.scrollLeft(1) // window moves one bucket earlier → content shifts right
					if m.viewportXOffset == prevOff {
						t.Fatalf("step %d: scroll clamped at offset %d — the sweep never left the edge",
							step, prevOff)
					}
					after := plotRows(&m)
					if len(before) == 0 || len(before) != len(after) {
						t.Fatalf("step %d row count: before=%d after=%d", step, len(before), len(after))
					}
					for i := range before {
						b, a := []rune(before[i]), []rune(after[i])
						if len(b) != len(a) {
							t.Fatalf("step %d row %d width: before=%d after=%d", step, i, len(b), len(a))
						}
						for k := stride + edge; k < len(a)-edge; k++ {
							if a[k] != b[k-stride] {
								t.Fatalf("step %d row %d col %d: after=%q, want before[%d]=%q — scrolling redrew the line instead of translating it by %d\nbefore: %s\nafter:  %s",
									step, i, k, a[k], k-stride, b[k-stride], stride, before[i], after[i])
							}
						}
					}
					before = after
				}
			})
		}
	}
}

// bodyPlotRows splits a buildLineChart result into its plot rows, dropping the
// x-label row. Dropping it is the point: that row is full-width text, so a
// scan that included it would report the frame as inked to the right edge no
// matter what the plot did.
func bodyPlotRows(body string) []string {
	rows := strings.Split(ansi.Strip(body), "\n")
	if len(rows) > 1 {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// lastInkedCol returns the index of the rightmost non-space column across the
// plot rows of body. Callers compare it against the width they ASKED for, not
// against the widest rendered row: rows carry no trailing padding, so a row
// that stops short is also a shorter row, and comparing ink to row length
// would always agree with itself.
func lastInkedCol(body string) int {
	last := -1
	for _, row := range bodyPlotRows(body) {
		r := []rune(row)
		for k := len(r) - 1; k >= 0; k-- {
			if r[k] != ' ' {
				last = max(last, k)
				break
			}
		}
	}
	return last
}

// TestBuildLineChart_PaintsToTheRightEdge guards the right terminus.
//
// The chart is pinned right by default (#306), so the rightmost column is
// "now" — the single most-read column on the usage view. ntcharts maps a
// point at exactly `to` one dot past the end of the braille grid, where
// PatternDotsGrid.Set drops it, so the terminus survives only because
// DrawBrailleDataSets draws SEGMENTS and the segment's intermediate dots fill
// the last cell. That is easy to break by adjusting the x-range, which #528
// does. Both the zero-samples baseline (two synthetic points, from and to) and
// a real series must still ink the final column.
func TestBuildLineChart_PaintsToTheRightEdge(t *testing.T) {
	withForcedColor(t)
	const vpW = 120
	zoom := ZoomLevels[0]
	from := lineWindowNow.Add(-vpW * zoom.Duration)
	to := lineWindowNow

	// Smoke-level only, deliberately. A flat two-point baseline is drawn as ONE
	// segment whose endpoint is clipped, so it inks every dot up to 2w-1 no
	// matter what the x-range does — no range change can make this subtest fail,
	// and it is kept for the no-samples path being painted at all, not as a
	// guard on the range. The real-series subtest below is what pins the edge.
	t.Run("zero-samples baseline", func(t *testing.T) {
		body := buildLineChart(nil, nil, from, to, vpW, 33, lineWindowNow, zoom, dateOrderDayFirst, "test", "")
		if last := lastInkedCol(body); last != vpW-1 {
			t.Fatalf("baseline inked up to column %d, want %d — the flat 100%% line stops short of the right edge", last, vpW-1)
		}
	})

	// BOTH series carry real points. An empty series is not a neutral choice
	// here: buildLineChart substitutes a flat two-point baseline for it, which
	// inks every column and would mask a real series that stopped short.
	t.Run("real series ending at now", func(t *testing.T) {
		// The series ends on the last BUCKET START, not at `to` itself: that is
		// what real data looks like (a sample lands in a bucket, not on the
		// frame's right edge), and a point sitting exactly on `to` re-inks the
		// final column on its own, hiding whether the scale reaches it.
		var pts []cache.UtilizationPoint
		for c := range vpW {
			pts = append(pts, cache.UtilizationPoint{At: from.Add(time.Duration(c) * zoom.Duration), Pct: float64(20 + c%50)})
		}
		body := buildLineChart(pts, pts, from, to, vpW, 33, lineWindowNow, zoom, dateOrderDayFirst, "test", "")
		if last := lastInkedCol(body); last != vpW-1 {
			t.Errorf("series inked up to column %d, want %d — the line stops short of the right edge, where \"now\" is", last, vpW-1)
		}
	})
}

// TestDotAlignedEnd_WideTerminal guards the dot-alignment arithmetic against
// int64 overflow.
//
// The shrink is a nanosecond multiply, and the window it is applied to grows
// with the terminal: at the 24h zoom a column is a day/12, so an 1,135-column
// terminal spans ~94 days ≈ 8.1e15 ns, and multiplying that by 2w-1 = 2,269
// passes 2^63. The wrap is silent and lands in one of two regimes, neither of
// which panics — which is exactly why it needs a test rather than a crash
// report:
//
//   - the result falls BEFORE from, ntcharts' SetViewXRange no-ops (it guards
//     with `if vMin < vMax`), and the 2-dots-per-column invariant this whole
//     change exists to establish is silently lost — the scroll re-rastering of
//     #528 returns with no error and no failing test;
//   - the result falls INSIDE the window, the bad range IS applied, and a few
//     hours of data get stretched across the full width under a label row that
//     still says weeks.
//
// The invariant asserted here is the definition: the aligned span is the
// window minus exactly one dot, i.e. d - d/(2w), and it always lies in
// (from, to].
func TestDotAlignedEnd_WideTerminal(t *testing.T) {
	from := lineWindowNow

	for _, tc := range []struct {
		name   string
		zoom   ZoomLevel
		chartW int
	}{
		{"24h/narrow", ZoomLevels[2], 200},
		{"24h/wide", ZoomLevels[2], 801},
		{"24h/ultrawide", ZoomLevels[2], 1_135},
		{"24h/absurd", ZoomLevels[2], 3_204},
		{"1h/ultrawide", ZoomLevels[1], 1_603},
		{"15m/ultrawide", ZoomLevels[0], 3_204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The window a viewport of this width actually spans at this zoom:
			// chartW columns is chartW/stride buckets.
			span := time.Duration(tc.chartW/tc.zoom.stride()) * tc.zoom.Duration
			to := from.Add(span)

			got := dotAlignedEnd(from, to, tc.chartW)

			if !got.After(from) || got.After(to) {
				t.Fatalf("dotAlignedEnd = %v, want within (%v, %v] — a %d-column terminal spans %v here and the shrink overflowed",
					got, from, to, tc.chartW, span)
			}
			if want := to.Add(-(span / time.Duration(2*tc.chartW))); !got.Equal(want) {
				t.Errorf("dotAlignedEnd = %v, want %v (one dot short of %v; off by %v)",
					got, want, to, got.Sub(want))
			}
		})
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
// (SetXOffset clamps to longestLineWidth-Width), so the spring's own frame must
// come through untouched.
//
// Asserted as frame equality, not as "not blank": the offset bubbles applies is
// unexported in v1, and "not blank" would pass on a completely wrong frame. The
// contract — the keypress moves the logical offset and paints nothing — is
// falsified by dropping the !springActive gate in scrollLeft, which is what
// makes this a guard rather than a smoke test.
func TestScroll_Remaining_DuringSpringNeverBlanks(t *testing.T) {
	withForcedColor(t)
	m, c := seedWideRemainingModel(t, 600, 120, lineWindowNow)
	defer c.Close()
	m.scrollLeft(40)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if !m.springActive {
		t.Fatal("u-toggle did not arm a spring — this guard needs one, so a silent skip would hide the regression it exists for")
	}
	before, beforeOff := m.viewport.View(), m.viewportXOffset

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)

	if strings.TrimSpace(ansi.Strip(m.viewport.View())) == "" {
		t.Fatal("viewport blanked after a scroll keypress during the spring")
	}
	if m.viewportXOffset == beforeOff {
		t.Errorf("viewportXOffset stayed %d — the keypress must still advance the logical offset so the post-settle refresh picks it up", beforeOff)
	}
	if got := m.viewport.View(); got != before {
		t.Error("the spring's frame changed on a scroll keypress — mid-spring scroll must move the logical offset only, leaving the animation to own the paint")
	}
}

// newPreResizeRemainingModel builds a remaining-mode model in the state the TUI
// is in before bubbletea's first tea.WindowSizeMsg: m.w == 0, so chartWidth()
// floors at 10, while viewport.Width is still the 80 that New's
// viewport.New(80, 20) left there. refreshChart genuinely runs in this state —
// the startup RefreshMsg can race ahead of the first resize (see maybeArmIntro).
// The usage history is a handful of recent samples and no messages, so the
// #300 padding (sized from chartWidth()) yields a logical canvas far narrower
// than the 80-column viewport.
func newPreResizeRemainingModel(t *testing.T, zoomIdx int, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	for i := range 8 {
		when := now.Add(-time.Duration(i) * 15 * time.Minute)
		resets := when.Add(2 * time.Hour)
		u := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 20 + float64(5*i), ResetsAt: &resets}}
		if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitRemaining)
	m.zoomIdx = zoomIdx
	m.now = func() time.Time { return now }
	m.refreshChart()
	return m, c
}

// TestRenderLineWindow_LabelCutAndPlotWindowShareCanvas pins #540: the label
// row renderLineWindow cuts and the plot window visibleWindow hands it must be
// two slices of ONE logical canvas. The label row is rendered at m.lastCanvasW
// over [lastChartFrom, lastChartTo] and cut at [xOff, xOff+vpW); the plot is
// drawn at vpW columns over visibleWindow(). So the cut must be exactly vpW
// columns wide, and the plot window must start and end at the instants the
// cut's two edges map to on that canvas.
//
// Until #540 the two sides derived the canvas width independently —
// refreshChart floored it at chartWidth(), visibleWindow at viewport.Width —
// and agreed only because handleWindowSize keeps those equal. The pre-resize
// cases pull them apart: the label row was an 11-column canvas while the plot
// spread the same span over 80 columns. View() renders nothing at m.w == 0,
// which is how that went unnoticed. The resized cases guard the steady state,
// including the 24h stride overshoot visibleXOffset clamps (#528).
func TestRenderLineWindow_LabelCutAndPlotWindowShareCanvas(t *testing.T) {
	type buildFn func(t *testing.T) (Model, *cache.Cache)
	type testCase struct {
		name  string
		build buildFn
	}
	var cases []testCase
	for zi, z := range ZoomLevels {
		cases = append(cases, testCase{
			name: "pre-resize/" + z.Label,
			build: func(t *testing.T) (Model, *cache.Cache) {
				m, c := newPreResizeRemainingModel(t, zi, lineWindowNow)
				if m.w != 0 || m.viewport.Width == m.chartWidth() {
					t.Fatalf("precondition: want the pre-resize state (m.w == 0, viewport.Width != chartWidth()), got m.w=%d viewport.Width=%d chartWidth()=%d",
						m.w, m.viewport.Width, m.chartWidth())
				}
				return m, c
			},
		})
		for _, pos := range []string{"pinned-right", "middle"} {
			cases = append(cases, testCase{
				name: "resized/" + z.Label + "/" + pos,
				build: func(t *testing.T) (Model, *cache.Cache) {
					// 4,000 15m-buckets ≈ 41 days: wide at every zoom (24h → ~490 cols).
					m, c := seedWideRemainingModel(t, 4_000, 400, lineWindowNow)
					m.zoomIdx = zi
					m.refreshChart()
					if pos == "middle" {
						m.scrollLeft(m.viewportXOffset / 2)
					}
					return m, c
				},
			})
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, c := tc.build(t)
			defer c.Close()

			vpW, canvasW := m.viewport.Width, m.lastCanvasW
			from, to := m.lastChartFrom, m.lastChartTo
			if got := lipgloss.Width(m.lineLabelRow); got != canvasW {
				t.Fatalf("precondition: label row is %d columns, want the logical canvas width %d", got, canvasW)
			}

			// The cut renderLineWindow takes. It must cover every plot column.
			xOff := m.visibleXOffset(canvasW)
			if got := lipgloss.Width(ansi.Cut(m.lineLabelRow, xOff, xOff+vpW)); got != vpW {
				t.Errorf("label cut [%d, %d) of the %d-column canvas is %d columns wide, want the plot's %d",
					xOff, xOff+vpW, canvasW, got, vpW)
			}

			// The instant at column col of the canvas the label row was rendered
			// on — the plain linear map, no clamp, so a window running past the
			// canvas edge shows up as a time past lastChartTo.
			spanSec := int64(to.Sub(from) / time.Second)
			at := func(col int) time.Time {
				return from.Add(time.Duration(spanSec*int64(col)/int64(canvasW)) * time.Second)
			}
			// columnToTime rounds to the nearest second, at truncates: allow 1s.
			near := func(a, b time.Time) bool { return a.Sub(b).Abs() <= time.Second }

			viewFrom, viewTo := m.visibleWindow()
			if want := at(xOff); !near(viewFrom, want) {
				t.Errorf("plot window starts at %v, want %v — the label cut's left edge (column %d of a %d-column canvas)",
					viewFrom, want, xOff, canvasW)
			}
			if want := at(xOff + vpW); !near(viewTo, want) {
				t.Errorf("plot window ends at %v, want %v — the label cut's right edge (column %d of a %d-column canvas)",
					viewTo, want, xOff+vpW, canvasW)
			}
		})
	}
}
