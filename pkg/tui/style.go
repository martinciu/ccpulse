package tui

import (
	"math"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Discrete TUI palette colors mapped to ANSI slots 0–15. The terminal's
// configured theme (Catppuccin, Dracula, Gruvbox, …) drives
// the actual rendered RGB. Names are kept color-based at this layer;
// semantic wrappers (burnSafeStyle, …) live where consumed.
var (
	Red    = lipgloss.Color("1") // severity: danger / over-limit
	Yellow = lipgloss.Color("3") // severity: warning / watch
	Green  = lipgloss.Color("2") // severity: safe / ok
	Dim    = lipgloss.Color("8") // borders, separators, dim labels, empty-state copy
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
// Dim (ANSI 8), ANSI 0 — over a 1.2 s window before disappearing. The fade
// is a tick-driven state machine on Model (indexFadeStop), not harmonica;
// the discrete stops match the issue spec and the per-stop dwell stays
// uniform.
const (
	indexFadeStopCount    = 3
	indexFadeStepDuration = 400 * time.Millisecond
)

// indexFadeStyle returns the lipgloss style for the supplied fade stop.
// stop=1 uses the terminal's default foreground (no Foreground call);
// stops 2 and 3 step down through Dim (ANSI 8) and ANSI 0. ANSI 0 sits
// near the terminal's background on curated themes, intentionally chosen
// as the penultimate near-invisible step before the indicator disappears.
// Any out-of-range stop falls back to ANSI 0 — defensive only;
// renderIndicators gates on FadeStop > 0 && FadeStop <= indexFadeStopCount.
func indexFadeStyle(stop int) lipgloss.Style {
	switch stop {
	case 1:
		return lipgloss.NewStyle()
	case 2:
		return lipgloss.NewStyle().Foreground(Dim)
	case 3:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("0"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("0"))
	}
}

// Y-label fade for the two-phase unit-toggle animation (issue #136).
// 5 stops defined as lipgloss.CompleteColor so termenv picks the
// highest-fidelity rendering the terminal advertises:
//   - Truecolor / 256-color: smooth 5-level grey fade via hex / 232–255.
//   - ANSI 16-color: pairs collapse to default fg / ANSI 8 / ANSI 0 —
//     3 distinct visible levels, same granularity indexFadeStyle gives.
//
// Stop 1 deliberately uses no Foreground call (matches indexFadeStyle's
// stop-1 precedent) so the label at full opacity matches surrounding
// chart text colour against the user's theme exactly.
var labelFadeStops = []lipgloss.CompleteColor{
	{},                                                // stop 1 — sentinel; no Foreground
	{TrueColor: "#888888", ANSI256: "244", ANSI: "8"}, // stop 2
	{TrueColor: "#555555", ANSI256: "240", ANSI: "8"}, // stop 3
	{TrueColor: "#333333", ANSI256: "236", ANSI: "0"}, // stop 4
	{TrueColor: "#111111", ANSI256: "232", ANSI: "0"}, // stop 5 — faintest
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
	stop := int(math.Ceil(fade * float64(labelFadeStopCount)))
	if stop < 1 {
		stop = 1
	}
	if stop > labelFadeStopCount {
		stop = labelFadeStopCount
	}
	if stop == 1 {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(labelFadeStops[stop-1])
}
