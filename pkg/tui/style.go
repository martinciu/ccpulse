package tui

import "github.com/charmbracelet/lipgloss"

// Solarized Dark.
var (
	Base03  = lipgloss.Color("#002b36")
	Base02  = lipgloss.Color("#073642")
	Base01  = lipgloss.Color("#586e75")
	Base00  = lipgloss.Color("#657b83")
	Base0   = lipgloss.Color("#839496")
	Base1   = lipgloss.Color("#93a1a1")
	Base2   = lipgloss.Color("#eee8d5")
	Base3   = lipgloss.Color("#fdf6e3")
	Yellow  = lipgloss.Color("#b58900")
	Orange  = lipgloss.Color("#cb4b16")
	Red     = lipgloss.Color("#dc322f")
	Magenta = lipgloss.Color("#d33682")
	Violet  = lipgloss.Color("#6c71c4")
	Blue    = lipgloss.Color("#268bd2")
	Cyan    = lipgloss.Color("#2aa198")
	Green   = lipgloss.Color("#859900")
)

type Style struct {
	Header    lipgloss.Style
	Tab       lipgloss.Style
	TabActive lipgloss.Style
	Footer    lipgloss.Style
}

func DefaultStyle(accent lipgloss.Color) Style {
	return Style{
		Header: lipgloss.NewStyle().Foreground(Base1).Background(Base02).Padding(0, 1),
		Tab:    lipgloss.NewStyle().Foreground(Base0).Padding(0, 2),
		TabActive: lipgloss.NewStyle().Foreground(accent).Bold(true).
			Underline(true).Padding(0, 2),
		Footer: lipgloss.NewStyle().Foreground(Base01).Background(Base02).Padding(0, 1),
	}
}
