package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all keybindings for the chart view.
type KeyMap struct {
	ScrollLeft  key.Binding
	ScrollRight key.Binding
	Zoom        key.Binding
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

// ShortHelp implements help.KeyMap.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollLeft, k.ScrollRight, k.Zoom, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ScrollLeft, k.ScrollRight},
		{k.Zoom, k.Help, k.Quit},
	}
}
