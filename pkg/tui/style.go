package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Solarized Dark palette — only constants actually referenced in the TUI
// are kept. Add new entries here when a new color is needed elsewhere.
var (
	Base02 = lipgloss.Color("#073642")
	Base01 = lipgloss.Color("#586e75")
	Yellow = lipgloss.Color("#b58900")
	Red    = lipgloss.Color("#dc322f")
	Green  = lipgloss.Color("#859900")
)

// Quota gradient stops, used by pkg/tui/model.go's newProgressBar. Hex
// strings (not lipgloss.Color) because bubbles/progress.WithGradient
// takes hex strings. Mirrors the Green / Red lipgloss.Color constants
// above so the chart and quota bar share the same heat ramp endpoints.
const (
	QuotaGradientStart = "#859900" // Solarized green
	QuotaGradientEnd   = "#dc322f" // Solarized red
)

// Index-completed fade animation. After backfill finishes, the indicator
// shows "✓ indexed N" and steps through three foregrounds — default fg,
// Base01, Base02 — over a 1.2 s window before disappearing. The fade is
// a tick-driven state machine on Model (indexFadeStop), not harmonica;
// the discrete stops match the issue spec and the per-stop dwell stays
// uniform.
const (
	indexFadeStopCount    = 3
	indexFadeStepDuration = 400 * time.Millisecond
)

// indexFadeStyle returns the lipgloss style for the supplied fade stop.
// stop=1 uses the terminal's default foreground (no Foreground call);
// stops 2 and 3 step down through the dim palette. Any out-of-range
// stop falls back to Base02 — defensive only; renderIndicators gates
// on FadeStop > 0 && FadeStop <= indexFadeStopCount.
func indexFadeStyle(stop int) lipgloss.Style {
	switch stop {
	case 1:
		return lipgloss.NewStyle()
	case 2:
		return lipgloss.NewStyle().Foreground(Base01)
	case 3:
		return lipgloss.NewStyle().Foreground(Base02)
	default:
		return lipgloss.NewStyle().Foreground(Base02)
	}
}
