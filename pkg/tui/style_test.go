package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLabelFadeStyle(t *testing.T) {
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

	// Direction binding: fade = 1.0 = full opacity = colorMuted (#335) so every
	// label reads as muted chrome matching the X-axis tick row, with no
	// brightness pop at the fade endpoints.
	wantMuted := lipgloss.NewStyle().Foreground(colorMuted).Render(probe)
	if got := labelFadeStyle(1.0).Render(probe); got != wantMuted {
		t.Errorf("labelFadeStyle(1.0).Render = %q, want %q (full opacity = colorMuted)", got, wantMuted)
	}
	// fade clamps above 1.0 still map to colorMuted.
	if got := labelFadeStyle(2.0).Render(probe); got != wantMuted {
		t.Errorf("labelFadeStyle(2.0).Render = %q, want %q (clamped to colorMuted)", got, wantMuted)
	}

	// Sub-full fade values produce SGR-wrapped output that still contains the
	// glyphs (the foreground is blended toward the background, not blanked).
	for _, fade := range []float64{0.75, 0.5, 0.25, 0.001} {
		got := labelFadeStyle(fade).Render(probe)
		if got == plain {
			t.Errorf("labelFadeStyle(%v).Render = %q; expected SGR-wrapped output", fade, got)
		}
		if !strings.Contains(got, plain) {
			t.Errorf("labelFadeStyle(%v).Render = %q does not contain probe %q", fade, got, plain)
		}
	}
}

// labelFadeStyle's sub-full frames blend the muted label color toward the
// detected terminal background, so setting a distinctive background must change
// the rendered fade color — while the full-opacity and sentinel endpoints stay
// fixed.
func TestLabelFadeStyle_FollowsSetBackground(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, true)

	// labelFadeTarget is process-global; save and restore it.
	prev := labelFadeTarget
	t.Cleanup(func() { labelFadeTarget = prev })

	const probe = "00:00"

	// Fallback (no background set) blends toward colorFaint.
	labelFadeTarget.ok = false
	fallback := labelFadeStyle(0.25).Render(probe)

	// With a distinctive background set, the same fade renders a different color.
	SetLabelFadeBackground("#002b36") // Solarized base03
	withBg := labelFadeStyle(0.25).Render(probe)
	if fallback == withBg {
		t.Errorf("sub-full fade ignored the set background: fallback=%q withBg=%q", fallback, withBg)
	}

	// Endpoints are unaffected by the target: full opacity stays muted chrome,
	// and the sentinel stays plain.
	if got, want := labelFadeStyle(1.0).Render(probe), lipgloss.NewStyle().Foreground(colorMuted).Render(probe); got != want {
		t.Errorf("fade=1.0 with background set = %q, want colorMuted %q", got, want)
	}
	if got := labelFadeStyle(0).Render(probe); got != probe {
		t.Errorf("fade=0 with background set = %q, want plain sentinel %q", got, probe)
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
