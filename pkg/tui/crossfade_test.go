package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestSynthLabelStarts(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)
	got := synthLabelStarts(from, to, ZoomLevels[1]) // 1h duration

	want := []time.Time{from, from.Add(time.Hour), from.Add(2 * time.Hour)}
	if len(got) != len(want) {
		t.Fatalf("synthLabelStarts len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("start[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSynthLabelStarts_EmptyWindowYieldsOne(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	// to == from: the loop body never runs, but max(...,1) guarantees cap≥1;
	// the slice is still empty (no t.Before(to) iteration). Asserts no panic
	// and a well-defined empty result.
	got := synthLabelStarts(from, from, ZoomLevels[0])
	if len(got) != 0 {
		t.Errorf("synthLabelStarts(from,from) = %v, want empty", got)
	}
	_ = ansi.Strip // keep the ansi import used across the file
}

func TestRenderXLabels_WrapsBuildXLabelsRow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	from := now.Add(-6 * time.Hour)
	to := now
	starts := synthLabelStarts(from, to, ZoomLevels[0]) // 15m
	chartW := 96

	raw := buildXLabelsRow(starts, chartW, ZoomLevels[0], now, dateOrderDayFirst)
	styled := renderXLabels(starts, chartW, ZoomLevels[0], now, dateOrderDayFirst)

	// renderXLabels is exactly dimStyle.Render(buildXLabelsRow(...)).
	if styled != dimStyle.Render(raw) {
		t.Errorf("renderXLabels != dimStyle.Render(buildXLabelsRow)\n got: %q\nwant: %q", styled, dimStyle.Render(raw))
	}
	// The raw row carries no ANSI styling and is chartW wide.
	if ansi.Strip(raw) != raw {
		t.Errorf("buildXLabelsRow returned styled output, want raw glyphs: %q", raw)
	}
	if len([]rune(raw)) != chartW {
		t.Errorf("raw row width = %d runes, want %d", len([]rune(raw)), chartW)
	}
}

func TestBuildLineChart_LabelRowOverride(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	from := now.Add(-6 * time.Hour)
	to := now
	chartW, chartH := 96, 10 // chartH >= 6 → showXLabels true

	override := "OVERRIDE-MARKER"
	body := buildLineChart(nil, nil, from, to, chartW, chartH, now, ZoomLevels[0], dateOrderDayFirst, "test", override)

	// The override string is spliced verbatim as the label row (last line).
	if !strings.Contains(body, override) {
		t.Errorf("buildLineChart did not splice labelRow override; body:\n%s", body)
	}

	// Empty override → internal renderXLabels path (no marker, behavior as before).
	body0 := buildLineChart(nil, nil, from, to, chartW, chartH, now, ZoomLevels[0], dateOrderDayFirst, "test", "")
	if strings.Contains(body0, override) {
		t.Errorf("empty labelRow should not contain marker")
	}
}

func TestLabelCadenceEqual(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	// A window spanning several 3-hour marks AND a midnight so the two real
	// cadences diverge: 15m emits 3-hourly clock ticks that 1h omits.
	from := time.Date(2026, 5, 19, 21, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 20, 6, 0, 0, 0, time.UTC)
	chartW := 96

	// Same zoom ⇒ identical row ⇒ cadence equal.
	if !labelCadenceEqual(ZoomLevels[1], ZoomLevels[1], from, to, chartW, now, dateOrderDayFirst) {
		t.Errorf("labelCadenceEqual(1h, 1h) = false, want true")
	}
	// 15m vs 1h ⇒ different cadence over this window ⇒ not equal.
	if labelCadenceEqual(ZoomLevels[0], ZoomLevels[1], from, to, chartW, now, dateOrderDayFirst) {
		t.Errorf("labelCadenceEqual(15m, 1h) = true, want false")
	}
}
