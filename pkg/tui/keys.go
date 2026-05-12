package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all keybindings for the chart view.
type KeyMap struct {
	ScrollLeft  key.Binding
	ScrollRight key.Binding
	Zoom        key.Binding
	Unit        key.Binding
	Help        key.Binding
	Quit        key.Binding
}

func defaultKeyMap() KeyMap {
	return KeyMap{
		ScrollLeft: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "scroll left"),
		),
		ScrollRight: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "scroll right"),
		),
		Zoom: key.NewBinding(
			key.WithKeys("z"),
			key.WithHelp("z", "zoom"),
		),
		Unit: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "unit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp implements help.KeyMap. Order: scroll, zoom, unit, help, quit.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollLeft, k.ScrollRight, k.Zoom, k.Unit, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap. Unit shares a row with Zoom — they're
// both "what the chart shows" toggles, distinct from "how the chart is
// scrolled" (top row).
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ScrollLeft, k.ScrollRight},
		{k.Zoom, k.Unit, k.Help, k.Quit},
	}
}
