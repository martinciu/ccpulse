package tui

import (
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

// barProfile pins the bar's termenv profile to whatever lipgloss is
// currently configured for. bubbles/progress v1 captures
// termenv.ColorProfile() at construction time, which bypasses
// lipgloss.SetColorProfile and renders Ascii under `go test`. Routing
// through lipgloss.ColorProfile() keeps tests deterministic
// (withForcedColor sets the lipgloss profile) and is a no-op in
// production where both detections resolve to the same value.
func barProfile() progress.Option {
	return progress.WithColorProfile(lipgloss.ColorProfile())
}

// resolveAdaptive returns the hex string from c that matches
// lipgloss.DefaultRenderer's detected background. bubbles/progress
// v1 only accepts string hex via WithSolidFill, so AdaptiveColor
// values need to be flattened before being passed to the bar.
func resolveAdaptive(c lipgloss.AdaptiveColor) string {
	if lipgloss.DefaultRenderer().HasDarkBackground() {
		return c.Dark
	}
	return c.Light
}

// barForSeverity returns a progress.Model whose fill matches the
// supplied burn severity:
//
//   - burnSeverityNoData / burnSeverityWarmingUp: green->red gradient
//     (the pre-projection look — identical to newProgressBar).
//   - burnSeveritySafe / Watch / Danger: solid fill in the matching
//     AdaptiveColor (the same colorSafe / colorWatch / colorDanger
//     the burn-rate row uses), so the bar and the burn-rate text
//     always render in the same hex on every theme.
//
// Width is clamped to >= 10 to match newProgressBar's contract.
func barForSeverity(width int, sev burnSeverity) progress.Model {
	switch sev {
	case burnSeveritySafe:
		return solidBar(width, resolveAdaptive(colorSafe))
	case burnSeverityWatch:
		return solidBar(width, resolveAdaptive(colorWatch))
	case burnSeverityDanger:
		return solidBar(width, resolveAdaptive(colorDanger))
	default:
		return newProgressBar(width)
	}
}

func solidBar(width int, hex string) progress.Model {
	if width < 10 {
		width = 10
	}
	return progress.New(
		progress.WithWidth(width),
		progress.WithoutPercentage(),
		progress.WithSolidFill(hex),
		barProfile(),
	)
}
