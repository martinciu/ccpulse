package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all keybindings for the chart view.
type KeyMap struct {
	ScrollLeft  key.Binding
	ScrollRight key.Binding
	Zoom        key.Binding
	Unit        key.Binding
	Projects    key.Binding
	Models      key.Binding
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
			key.WithHelp("u", "cost/output/usage"),
		),
		Projects: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "projects"),
		),
		Models: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "models"),
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

// ShortHelp implements help.KeyMap. Order: scroll, zoom, unit, projects,
// models, help, quit. Note that help.Model truncates from the right at the
// first binding that does not fit, so "m models" is hidden below ~89 columns;
// the ? overlay (FullHelp) is column-based and always shows it.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollLeft, k.ScrollRight, k.Zoom, k.Unit, k.Projects, k.Models, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap. Unit, Projects and Models share a row with
// Zoom — they're all "what the chart shows" toggles, distinct from "how the
// chart is scrolled" (top row).
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ScrollLeft, k.ScrollRight},
		{k.Zoom, k.Unit, k.Projects, k.Models, k.Help, k.Quit},
	}
}
