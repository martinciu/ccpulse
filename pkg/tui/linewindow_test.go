package tui

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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
