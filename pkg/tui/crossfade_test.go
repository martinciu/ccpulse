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

func TestBuildLineChart_LabelRowIgnoredBelowXLabelThreshold(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	from := now.Add(-6 * time.Hour)
	to := now
	chartW, chartH := 96, 4 // chartH < 6 → showXLabels false → labelRow must be dropped

	override := "OVERRIDE-MARKER"
	// Guard: if a future refactor splices labelRow before the showXLabels gate,
	// the marker would appear at chartH=4 and this assertion would catch it.
	body := buildLineChart(nil, nil, from, to, chartW, chartH, now, ZoomLevels[0], dateOrderDayFirst, "test", override)
	if strings.Contains(body, override) {
		t.Errorf("buildLineChart spliced labelRow at chartH=%d (below showXLabels threshold); marker must be absent; body:\n%s", chartH, body)
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

func TestCrossfadeLabelRow_Phases(t *testing.T) {
	// Asserts on distinct rendered ANSI (the r=0.25 dimmed-below-colorMuted
	// check), so it needs a forced color profile. withForcedColor mutates the
	// process-global renderer and is NOT safe with t.Parallel() — keep this
	// test sequential, mirroring TestLabelFadeStyle_Quantisation.
	withForcedColor(t)
	withForcedDarkBackground(t, true)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	chartW := 96
	// Old window spans 21:00→06:00 (15m cadence: 3-hourly ticks + midnight date).
	oFrom := time.Date(2026, 5, 19, 21, 0, 0, 0, time.UTC)
	oTo := time.Date(2026, 5, 20, 6, 0, 0, 0, time.UTC)
	// New (settled) window 1h cadence: midnight date only.
	nFrom := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	nTo := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	snap := zoomAnimSnapshot{
		oFrom: oFrom, oTo: oTo, nFrom: nFrom, nTo: nTo,
		oZoom: ZoomLevels[0], now: now, sameCadence: false,
	}
	newZoom := ZoomLevels[1]
	order := dateOrderDayFirst

	outRow := buildXLabelsRow(synthLabelStarts(oFrom, oTo, snap.oZoom), chartW, snap.oZoom, now, order)
	inRow := buildXLabelsRow(synthLabelStarts(nFrom, nTo, newZoom), chartW, newZoom, now, order)
	if ansi.Strip(outRow) == ansi.Strip(inRow) {
		t.Fatalf("test setup: outgoing and incoming rows identical — pick a discriminating window")
	}

	// r=0: outgoing at full opacity (== dimStyle, the handoff-parity endpoint).
	got0 := crossfadeLabelRow(snap, oFrom, oTo, newZoom, chartW, 0.0, order)
	if ansi.Strip(got0) != ansi.Strip(outRow) {
		t.Errorf("r=0 row glyphs = %q, want outgoing %q", ansi.Strip(got0), ansi.Strip(outRow))
	}
	if got0 != dimStyle.Render(outRow) {
		t.Errorf("r=0 not full-opacity (colorMuted); got %q want %q", got0, dimStyle.Render(outRow))
	}

	// r=1: incoming at full opacity. viewFrom/viewTo == settled new window.
	got1 := crossfadeLabelRow(snap, nFrom, nTo, newZoom, chartW, 1.0, order)
	if ansi.Strip(got1) != ansi.Strip(inRow) {
		t.Errorf("r=1 row glyphs = %q, want incoming %q", ansi.Strip(got1), ansi.Strip(inRow))
	}
	if got1 != dimStyle.Render(inRow) {
		t.Errorf("r=1 not full-opacity (colorMuted); got %q want %q", got1, dimStyle.Render(inRow))
	}

	// r=0.5: blank strip of spaces (the accepted near-blank midpoint).
	got5 := crossfadeLabelRow(snap, oFrom, oTo, newZoom, chartW, 0.5, order)
	if ansi.Strip(got5) != strings.Repeat(" ", chartW) {
		t.Errorf("r=0.5 row = %q, want %d spaces", ansi.Strip(got5), chartW)
	}

	// r=0.25: outgoing glyphs, but dimmed (NOT full-opacity colorMuted).
	got25 := crossfadeLabelRow(snap, oFrom, oTo, newZoom, chartW, 0.25, order)
	if ansi.Strip(got25) != ansi.Strip(outRow) {
		t.Errorf("r=0.25 glyphs = %q, want outgoing %q", ansi.Strip(got25), ansi.Strip(outRow))
	}
	if got25 == dimStyle.Render(outRow) {
		t.Errorf("r=0.25 should be dimmed below colorMuted, got full opacity")
	}
}

func TestCrossfadeLabelRow_SameCadenceRides(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	chartW := 96
	vf := now.Add(-6 * time.Hour)
	vt := now
	snap := zoomAnimSnapshot{
		oFrom: vf, oTo: vt, nFrom: vf, nTo: vt,
		oZoom: ZoomLevels[1], now: now, sameCadence: true,
	}
	// sameCadence ⇒ full-opacity incoming row at every r (no fade frames, no blank).
	for _, r := range []float64{0.0, 0.25, 0.5, 0.75, 1.0} {
		got := crossfadeLabelRow(snap, vf, vt, ZoomLevels[1], chartW, r, dateOrderDayFirst)
		inRow := buildXLabelsRow(synthLabelStarts(vf, vt, ZoomLevels[1]), chartW, ZoomLevels[1], now, dateOrderDayFirst)
		if got != dimStyle.Render(inRow) {
			t.Errorf("sameCadence r=%v: got %q, want full-opacity %q", r, got, dimStyle.Render(inRow))
		}
	}
}
