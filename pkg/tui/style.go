package tui

import (
	"math"
	"time"

	"github.com/charmbracelet/lipgloss"
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
	colorChartTokens       = lipgloss.AdaptiveColor{Light: "#1565c0", Dark: "#64b5f6"} // Material Blue 800 / 300
	colorChartCost         = lipgloss.AdaptiveColor{Light: "#ff8f00", Dark: "#ffca28"} // Material Amber 800 / 400
	colorChartRemaining5h  = lipgloss.AdaptiveColor{Light: "#2e7d32", Dark: "#81c784"} // Material Green 800 / 300
	colorChartRemaining7d  = lipgloss.AdaptiveColor{Light: "#6a1b9a", Dark: "#ce93d8"} // Material Purple 800 / 300
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

// Y-label fade for the two-phase unit-toggle animation (issue #136).
// 5 stops defined as lipgloss.CompleteAdaptiveColor so termenv picks the
// highest-fidelity rendering the terminal advertises AND picks a side
// (Light/Dark) that fades toward the user's actual background:
//   - Truecolor / 256-color: smooth 5-level grey fade. On dark themes the
//     ramp descends toward near-black; on light themes it ascends toward
//     near-white. Either way the faintest stop matches the background.
//   - ANSI 16-color: stops collapse to default fg / ANSI 8 / ANSI 0 on
//     dark and default fg / ANSI 8 / ANSI 7 / ANSI 15 on light.
//
// Stop 1 deliberately uses no Foreground call (matches indexFadeStyle's
// stop-1 precedent) so the label at full opacity matches surrounding
// chart text colour against the user's theme exactly.
var labelFadeStops = []lipgloss.CompleteAdaptiveColor{
	{}, // stop 1 — sentinel; labelFadeStyle skips Foreground at stop 1
	{
		Light: lipgloss.CompleteColor{TrueColor: "#777777", ANSI256: "244", ANSI: "8"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#888888", ANSI256: "244", ANSI: "8"},
	},
	{
		Light: lipgloss.CompleteColor{TrueColor: "#aaaaaa", ANSI256: "248", ANSI: "7"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#555555", ANSI256: "240", ANSI: "8"},
	},
	{
		Light: lipgloss.CompleteColor{TrueColor: "#cccccc", ANSI256: "252", ANSI: "7"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#333333", ANSI256: "236", ANSI: "0"},
	},
	{
		Light: lipgloss.CompleteColor{TrueColor: "#eeeeee", ANSI256: "255", ANSI: "15"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#111111", ANSI256: "232", ANSI: "0"},
	},
}

const labelFadeStopCount = 5

// labelFadeStyle maps fade ∈ [0, 1] to a discrete lipgloss style.
//   - fade <= 0 → hidden sentinel (no Foreground). Caller gates render on fade > 0.
//   - 0 < fade  → bucket = ceil(fade * 5), clamped to [1, 5]. Stop 1 is brightest.
//
// Stop 1 uses no Foreground call (consistent with indexFadeStyle's stop 1).
// Stops 2–5 use CompleteColor; lipgloss/termenv downsamples per terminal
// profile, which can collapse adjacent stops on 16-color terminals — the
// degradation is documented in the spec.
func labelFadeStyle(fade float64) lipgloss.Style {
	if fade <= 0 {
		return lipgloss.NewStyle()
	}
	// Invert fade so high fade (near 1.0 = full opacity at steady state)
	// maps to stop 1 (brightest, no Foreground), and fade close to 0
	// maps to stop 5 (faintest, near-background). The Y-label gets
	// progressively darker as bars shrink (Phase 1) and progressively
	// brighter as bars grow (Phase 2), matching the spec's "synced with
	// max(springRatios)" intent.
	stop := int(math.Ceil((1.0 - fade) * float64(labelFadeStopCount)))
	stop = max(stop, 1)
	stop = min(stop, labelFadeStopCount)
	if stop == 1 {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(labelFadeStops[stop-1])
}
