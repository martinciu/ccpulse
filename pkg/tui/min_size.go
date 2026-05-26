package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// MinWidth / MinHeight are the lower bound for rendering the full ccpulse
// layout. Below either threshold, View() short-circuits to a centered
// "Terminal too small" notice (issue #356). 80×24 is the POSIX/VT100
// baseline — most TUIs (less, top, vim, htop) assume it, and macOS Terminal
// and iTerm2 default to 80×24 on first launch. The header box, chart day
// labels, and help footer all start fighting their boxes below this.
const (
	MinWidth  = 80
	MinHeight = 24
)

// renderTooSmall builds the centered "Terminal too small" notice shown when
// the terminal is below MinWidth × MinHeight. Title bolded, the two
// dimension lines dimmed with colorMuted. Centered on both axes via
// lipgloss.Place — no manual padding math.
func renderTooSmall(termW, termH int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Terminal too small")
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	need := dim.Render(fmt.Sprintf("Need: %d×%d", MinWidth, MinHeight))
	have := dim.Render(fmt.Sprintf("Have: %d×%d", termW, termH))
	block := lipgloss.JoinVertical(lipgloss.Center, title, need, have)
	return lipgloss.Place(termW, termH, lipgloss.Center, lipgloss.Center, block)
}
