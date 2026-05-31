package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Severity, chrome, and faint-stop tokens. Light/Dark stops feed
// lipgloss.AdaptiveColor — termenv profile degradation handles 16-color
// terminals automatically (no hand-mapped ANSI fallback needed). Promote to
// lipgloss.CompleteAdaptiveColor only if a visual probe surfaces a problem.
var (
	colorSafe   = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // Material green 700 / 300
	colorWatch  = lipgloss.AdaptiveColor{Light: "#ef6c00", Dark: "#ffb74d"} // Material orange 700 / 300
	colorDanger = lipgloss.AdaptiveColor{Light: "#c62828", Dark: "#e57373"} // Material red 700 / 300
	colorMuted  = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#9e9e9e"} // chrome, dim labels (Material grey 700 / 500)
	colorFaint  = lipgloss.AdaptiveColor{Light: "#bdbdbd", Dark: "#424242"} // fade-out endpoint (Material grey 400 / 800)

	// Unit-keyed chart bar colors — NOT severity. buildChart picks one
	// per chartUnit so the bar color tells the user which axis is plotted
	// (tokens vs cost), independent of bucket height. The header quota
	// bars keep the green→red gradient where the ramp IS meaningful.
	colorChartTokens      = lipgloss.AdaptiveColor{Light: "#1565c0", Dark: "#64b5f6"} // Material Blue 800 / 300
	colorChartCost        = lipgloss.AdaptiveColor{Light: "#ff8f00", Dark: "#ffca28"} // Material Amber 800 / 400
	colorChartRemaining5h = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // Material Green 800 / 300
	colorChartRemaining7d = lipgloss.AdaptiveColor{Light: "#6a1b9a", Dark: "#ce93d8"} // Material Purple 800 / 300

	// colorBarLabel is the knockout foreground for in-bar 24h numbers (#308).
	// Dark-theme bars are bright (amber #ffca28 / blue #64b5f6) -> near-black
	// text; light-theme bars are dark-saturated (#ff8f00 / #1565c0) -> near-
	// white text. Paired with Background(barColor) at the splice site.
	colorBarLabel = lipgloss.AdaptiveColor{Light: "#fafafa", Dark: "#0b0b0b"}
)

// Quota gradient stops. Hex (not ANSI slots) because bubbles/progress.WithGradient
// requires hex for RGB interpolation — emits 24-bit truecolor escapes per cell,
// palette-independent. Material Design 500 green/red: theme-neutral defaults that
// don't claim a brand identity on any target theme.
const (
	QuotaGradientStart = "#4caf50" // material green 500
	QuotaGradientEnd   = "#f44336" // material red 500
)

// Index-completed fade animation. After backfill finishes, the indicator
// shows "✓ indexed N" and steps through three foregrounds — default fg,
// colorMuted, colorFaint — over a 1.2 s window before disappearing. The
// fade is a tick-driven state machine on Model (indexFadeStop), not
// harmonica; the discrete stops match the issue spec and the per-stop
// dwell stays uniform.
const (
	indexFadeStopCount    = 3
	indexFadeStepDuration = 400 * time.Millisecond

	// indexBannerDwellDuration is the full-opacity dwell used when
	// reduce_motion is enabled. Equals the total span of the 3-step
	// fade ladder so the banner is visible for the same total time
	// regardless of mode — only whether it fades or stays solid changes.
	indexBannerDwellDuration = time.Duration(indexFadeStopCount) * indexFadeStepDuration
)

// indexFadeStyle returns the lipgloss style for the supplied fade stop.
// stop=1 uses the terminal's default foreground (no Foreground call);
// stops 2 and 3 step down through colorMuted (chrome) and colorFaint
// (near-bg). The fade is consumed by the post-backfill "✓ indexed N"
// indicator and runs over a 1.2 s window via tickFadeMsg.
//
// Any out-of-range stop falls back to colorFaint — defensive only;
// renderIndicators gates on FadeStop > 0 && FadeStop <= indexFadeStopCount.
func indexFadeStyle(stop int) lipgloss.Style {
	switch stop {
	case 1:
		return lipgloss.NewStyle()
	case 2:
		return lipgloss.NewStyle().Foreground(colorMuted)
	case 3:
		return lipgloss.NewStyle().Foreground(colorFaint)
	default:
		return lipgloss.NewStyle().Foreground(colorFaint)
	}
}

// labelFadeTarget is the color the label fade dissolves TOWARD — ideally the
// terminal's actual background, so the faintest step is genuinely invisible
// instead of a fixed near-black/near-white that only matches a pure black or
// white terminal (the earlier ramp darkened toward #111111, which reads as a
// dark smudge on any non-black background). Set once at startup by
// SetLabelFadeBackground, which cmd/ccpulse calls after querying the terminal
// and before the Bubble Tea program owns the tty. When unset (tests, non-TTY,
// terminals that don't answer the query), the fade falls back to colorFaint —
// a muted grey that is never darker than a typical background.
var labelFadeTarget struct {
	c  colorful.Color
	ok bool
}

// SetLabelFadeBackground records the detected terminal background (hex
// "#rrggbb") as the label-fade target. An empty or unparseable value leaves the
// colorFaint fallback in place. Call once at startup, before Bubble Tea takes
// over the terminal — it mutates process-global state read by labelFadeStyle.
func SetLabelFadeBackground(hex string) {
	c, err := colorful.Hex(hex)
	if err != nil {
		return
	}
	labelFadeTarget.c, labelFadeTarget.ok = c, true
}

// labelFadeStyle maps fade ∈ [0, 1] to the label foreground for one animation
// frame. Used by the zoom label cross-fade (chart.go) and the two-phase
// unit-toggle Y-label animation (issue #136).
//
//   - fade <= 0 → hidden sentinel (no Foreground). Callers gate render on
//     fade > 0 and emit a blank row themselves.
//   - fade >= 1 → colorMuted, so a fully-opaque label reads as muted chrome
//     matching the steady-state X-axis tick row (#335) with no brightness pop
//     at the fade endpoints.
//   - 0 < fade < 1 → the muted label color blended toward the terminal
//     background (labelFadeTarget, else colorFaint) in CIE-Lab space, so the
//     label dissolves smoothly into the background rather than darkening toward
//     a fixed near-black. Rendered as a truecolor foreground; termenv
//     downsamples per terminal profile.
func labelFadeStyle(fade float64) lipgloss.Style {
	if fade <= 0 {
		return lipgloss.NewStyle()
	}
	if fade >= 1 {
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
	from, to := labelFadeEndpoints()
	// BlendLab(to, t): t=0 → from, t=1 → to. fade→1 stays at the muted label,
	// fade→0 reaches the background, so the fade dissolves out as fade drops.
	c := from.BlendLab(to, 1.0-fade).Clamped()
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex()))
}

// labelFadeEndpoints resolves the fade's start (the muted label color) and end
// (the terminal background if detected, else colorFaint) to RGB for the active
// light/dark theme. The Light/Dark side is chosen by the same
// HasDarkBackground signal that drives every other AdaptiveColor in the UI, so
// the fade's start matches the steady-state muted label exactly.
func labelFadeEndpoints() (from, to colorful.Color) {
	dark := lipgloss.HasDarkBackground()
	from = hexColor(adaptiveSide(colorMuted, dark))
	if labelFadeTarget.ok {
		return from, labelFadeTarget.c
	}
	return from, hexColor(adaptiveSide(colorFaint, dark))
}

// adaptiveSide returns the Dark or Light hex of an AdaptiveColor token.
func adaptiveSide(c lipgloss.AdaptiveColor, dark bool) string {
	if dark {
		return c.Dark
	}
	return c.Light
}

// hexColor parses a "#rrggbb" token to a colorful.Color. The in-package color
// tokens are compile-time-valid 6-digit hex, so the error is ignored (a parse
// failure yields the zero color — black — a safe fade endpoint).
func hexColor(hex string) colorful.Color {
	c, _ := colorful.Hex(hex)
	return c
}
