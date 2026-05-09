package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/status"
)

type Tab int

const (
	TabLive Tab = iota
	TabToday
	TabHistory
	TabProjects
	TabModels
)

func (t Tab) String() string {
	return []string{"Live", "Today", "History", "Projects", "Models"}[t]
}

type Deps struct {
	// filled in over upcoming tasks: cache, config, status computer.
}

type Model struct {
	deps     Deps
	tab      Tab
	style    Style
	w, h     int
	window   status.Window
	showHelp bool
	live     []cache.LiveSession
}

func New(d Deps) Model {
	return Model{
		deps:  d,
		tab:   TabLive,
		style: DefaultStyle(Violet),
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.tab = (m.tab + 1) % 5
		case "shift+tab":
			m.tab = (m.tab + 4) % 5
		case "1", "2", "3", "4", "5":
			m.tab = Tab(msg.String()[0] - '1')
		case "?":
			m.showHelp = !m.showHelp
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.showHelp {
		return helpView(m.style)
	}
	width := m.w
	if width < 80 {
		width = 80
	}
	header := renderHeader(m.style, m.window, width)
	tabs := m.renderTabs()
	var body string
	switch m.tab {
	case TabLive:
		body = renderLive(m.live)
	default:
		body = "  <" + m.tab.String() + ">"
	}
	footer := m.style.Footer.Render("[tab]→  [j/k]nav  [r]efresh  [c]onfig  [?]help  [q]")
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, body, footer)
}

func (m Model) renderTabs() string {
	tabs := make([]string, 5)
	for i := 0; i < 5; i++ {
		t := Tab(i)
		if t == m.tab {
			tabs[i] = m.style.TabActive.Render(t.String())
		} else {
			tabs[i] = m.style.Tab.Render(t.String())
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}
