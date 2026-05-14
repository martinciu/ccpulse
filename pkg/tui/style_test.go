package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLabelFadeStyle_Quantisation(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, true)

	const probe = "12.3k"
	plain := probe

	// Sentinel cases: fade <= 0 returns body unchanged (no Foreground).
	if got := labelFadeStyle(0).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(0).Render = %q, want %q (sentinel)", got, plain)
	}
	if got := labelFadeStyle(-0.5).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(-0.5).Render = %q, want %q (sentinel; fade clamped)", got, plain)
	}

	// Direction binding: fade = 1.0 = full opacity = stop 1 = no
	// Foreground call. Rendering must leave the probe unchanged.
	// (Matches indexFadeStyle's stop-1 precedent.)
	if got := labelFadeStyle(1.0).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(1.0).Render = %q, want %q (stop 1 = no Foreground at full opacity)", got, plain)
	}
	// fade clamps above 1.0 still map to stop 1.
	if got := labelFadeStyle(2.0).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(2.0).Render = %q, want %q (clamped to stop 1)", got, plain)
	}

	// Mid and low fade values produce SGR-wrapped output (stops 2–5).
	// With ceil((1 - fade) * 5) the inclusive upper-bound boundaries are
	// fade=0.6 (stop 2), 0.4 (stop 3), 0.2 (stop 4), 0.001 (stop 5).
	for _, tc := range []struct {
		name string
		fade float64
	}{
		{"stop 2 boundary (0.6)", 0.6},
		{"stop 3 boundary (0.4)", 0.4},
		{"stop 4 boundary (0.2)", 0.2},
		{"stop 5 near-zero (0.001)", 0.001},
	} {
		got := labelFadeStyle(tc.fade).Render(probe)
		if got == plain {
			t.Errorf("%s: labelFadeStyle(%v).Render = %q; expected SGR-wrapped output",
				tc.name, tc.fade, got)
		}
		if !strings.Contains(got, plain) {
			t.Errorf("%s: labelFadeStyle(%v).Render = %q does not contain probe %q",
				tc.name, tc.fade, got, plain)
		}
	}

	// Stop count constant must match the slice length so callers can
	// rely on it as the bucket count.
	if got := len(labelFadeStops); got != labelFadeStopCount {
		t.Errorf("len(labelFadeStops) = %d, want %d", got, labelFadeStopCount)
	}
}

func TestAdaptiveTokens_LightDark(t *testing.T) {
	// Pins the Light/Dark hex stops for the five severity/chrome tokens.
	// Catches accidental Light/Dark swaps and hex drift across future edits.
	// Asserts on the AdaptiveColor struct fields directly — verifying lipgloss's
	// own HasDarkBackground routing is out of scope.
	cases := []struct {
		name      string
		token     lipgloss.AdaptiveColor
		wantLight string
		wantDark  string
	}{
		{"colorSafe", colorSafe, "#2e7d32", "#81c784"},
		{"colorWatch", colorWatch, "#ef6c00", "#ffb74d"},
		{"colorDanger", colorDanger, "#c62828", "#e57373"},
		{"colorMuted", colorMuted, "#666666", "#9e9e9e"},
		{"colorFaint", colorFaint, "#bdbdbd", "#424242"},
		{"colorChartTokens", colorChartTokens, "#1565c0", "#64b5f6"},
		{"colorChartCost", colorChartCost, "#ff8f00", "#ffca28"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.token.Light != c.wantLight {
				t.Errorf("%s.Light = %q, want %q", c.name, c.token.Light, c.wantLight)
			}
			if c.token.Dark != c.wantDark {
				t.Errorf("%s.Dark = %q, want %q", c.name, c.token.Dark, c.wantDark)
			}
		})
	}
}
