package tui

import (
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
