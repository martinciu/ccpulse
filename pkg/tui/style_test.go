package tui

import (
	"strings"
	"testing"
)

func TestLabelFadeStyle_Quantisation(t *testing.T) {
	// `go test` runs without a TTY, so lipgloss auto-strips colors by
	// default. Force TrueColor so the CompleteColor stops actually emit
	// SGR escapes the assertions below depend on.
	withForcedColor(t)

	const probe = "12.3k"
	// A style with no Foreground call renders its input unchanged
	// (no ANSI SGR escapes). Stop 1 + the sentinel both use no
	// Foreground; stops 2–5 wrap the input in SGR codes from the
	// CompleteColor downsampling, so the rendered length grows.
	plain := probe

	// fade <= 0 → hidden sentinel; render must be identical to plain.
	if got := labelFadeStyle(0).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(0).Render = %q, want %q (no Foreground for sentinel)", got, plain)
	}
	if got := labelFadeStyle(-0.5).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(-0.5).Render = %q, want %q (sentinel; fade clamped)", got, plain)
	}

	// 0 < fade ≤ 0.2 → stop 1; rendering must also leave the string
	// unchanged because stop 1 matches the indexFadeStyle precedent
	// (no Foreground call at full opacity).
	if got := labelFadeStyle(0.2).Render(probe); got != plain {
		t.Errorf("labelFadeStyle(0.2).Render = %q, want %q (stop 1 = no Foreground)", got, plain)
	}

	// 0.2 < fade ≤ 0.4 → stop 2; rendering must wrap the input in SGR
	// escapes from labelFadeStops[1]. Assert this indirectly: the
	// output must contain the input as a substring and be strictly
	// longer (SGR open + close around the probe).
	for _, tc := range []struct {
		name string
		fade float64
	}{
		{"stop 2 boundary", 0.4},
		{"stop 3 boundary", 0.6},
		{"stop 4 boundary", 0.8},
		{"stop 5 boundary", 1.0},
		{"stop 5 clamped", 2.0},
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
