package tui

import "github.com/charmbracelet/lipgloss"

// Solarized Dark palette — only constants actually referenced in the TUI
// are kept. Add new entries here when a new color is needed elsewhere.
var (
	Base02 = lipgloss.Color("#073642")
	Base01 = lipgloss.Color("#586e75")
	Yellow = lipgloss.Color("#b58900")
	Red    = lipgloss.Color("#dc322f")
	Green  = lipgloss.Color("#859900")
)
