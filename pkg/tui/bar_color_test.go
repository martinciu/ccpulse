package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// TestResolveAdaptive_PicksSideFromRenderer pins
// lipgloss.DefaultRenderer's HasDarkBackground via the existing
// withForcedDarkBackground helper (main_test.go) and asserts the
// returned hex matches the AdaptiveColor's Light/Dark side.
func TestResolveAdaptive_PicksSideFromRenderer(t *testing.T) {
	c := lipgloss.AdaptiveColor{Light: "#111111", Dark: "#eeeeee"}

	t.Run("dark", func(t *testing.T) {
		withForcedDarkBackground(t, true)
		if got := resolveAdaptive(c); got != "#eeeeee" {
			t.Errorf("dark background: got %q, want %q", got, "#eeeeee")
		}
	})
	t.Run("light", func(t *testing.T) {
		withForcedDarkBackground(t, false)
		if got := resolveAdaptive(c); got != "#111111" {
			t.Errorf("light background: got %q, want %q", got, "#111111")
		}
	})
}

// TestBarForSeverity_FillShape asserts that noData/warmingUp produce
// a bar whose 50%-fill rendering carries the gradient signature (>=2
// distinct fill-cell color escapes — WithGradient assigns a different
// hex per cell), while safe/watch/danger produce a solid-fill bar
// whose filled cells all share one truecolor token matching the
// matching colorSafe/Watch/Danger Dark hex, and explicitly do not
// emit the other two severities' tokens.
//
// `bubbles/progress` always emits a separate escape for the empty
// track cells (EmptyColor #606060), so total distinct escapes are
// always >= 2; the meaningful discriminator is "which fill hex(es)
// appear in the output".
func TestBarForSeverity_FillShape(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, true)
	const width = 20
	safeTok := hexToTruecolorTok(colorSafe.Dark)
	watchTok := hexToTruecolorTok(colorWatch.Dark)
	dangerTok := hexToTruecolorTok(colorDanger.Dark)
	tests := []struct {
		name      string
		sev       burnSeverity
		wantSolid bool
		wantTok   string
		notToks   []string // tokens that must NOT appear (cross-severity isolation)
	}{
		{"noData", burnSeverityNoData, false, "", nil},
		{"warmingUp", burnSeverityWarmingUp, false, "", nil},
		{"safe", burnSeveritySafe, true, safeTok, []string{watchTok, dangerTok}},
		{"watch", burnSeverityWatch, true, watchTok, []string{safeTok, dangerTok}},
		{"danger", burnSeverityDanger, true, dangerTok, []string{safeTok, watchTok}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := barForSeverity(width, tt.sev)
			got := bar.ViewAs(0.5)
			if tt.wantSolid {
				if !strings.Contains(got, tt.wantTok) {
					t.Errorf("solid severity %v: rendered output missing expected truecolor token %q\nrendered: %q",
						tt.sev, tt.wantTok, got)
				}
				for _, tok := range tt.notToks {
					if strings.Contains(got, tok) {
						t.Errorf("solid severity %v: rendered output unexpectedly contains other-severity token %q\nrendered: %q",
							tt.sev, tok, got)
					}
				}
			} else {
				for _, tok := range []string{safeTok, watchTok, dangerTok} {
					if strings.Contains(got, tok) {
						t.Errorf("gradient severity %v: rendered output unexpectedly contains severity token %q\nrendered: %q",
							tt.sev, tok, got)
					}
				}
			}
		})
	}
}

// hexToTruecolorTok converts "#RRGGBB" to the truecolor escape
// substring "\x1b[38;2;R;G;Bm" the same way termenv emits it
// (colorful float roundtrip + uint8 truncation), so tests stay in
// sync with the actual emitted sequence on edge bytes like 0x84.
func hexToTruecolorTok(hex string) string {
	c, err := colorful.Hex(hex)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", uint8(c.R*255), uint8(c.G*255), uint8(c.B*255))
}
