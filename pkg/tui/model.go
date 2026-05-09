package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/status"
)

// RefreshMsg is sent by the watcher loop to trigger a TUI re-query.
type RefreshMsg struct{}

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
	Cache        *cache.Cache
	ProjectsRoot string
	HistoryDays  int
}

type Model struct {
	deps         Deps
	tab          Tab
	style        Style
	w, h         int
	window       status.Window
	showHelp     bool
	live         []cache.LiveSession
	today        []cache.ModelTotals
	history      []cache.DayTotals
	projects     []cache.ProjectTotals
	models       []cache.ModelTotals
	modelsWindow cache.ModelsWindow
	drilled      bool
}

func New(d Deps) Model {
	return Model{
		deps:         d,
		tab:          TabLive,
		style:        DefaultStyle(Violet),
		modelsWindow: cache.WindowToday,
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case RefreshMsg:
		if m.deps.Cache == nil {
			return m, nil
		}
		now := time.Now()
		if w, err := computeWindowFromDeps(m.deps, now); err == nil {
			m.window = w
		}
		if rows, err := m.deps.Cache.LiveSessions(now, 24*time.Hour); err == nil {
			m.live = rows
		}
		if rows, err := m.deps.Cache.TodayByModel(now); err == nil {
			m.today = rows
		}
		if rows, err := m.deps.Cache.HistoryByDay(or30(m.deps.HistoryDays)); err == nil {
			m.history = rows
		}
		if rows, err := m.deps.Cache.ProjectsTotals(or30(m.deps.HistoryDays)); err == nil {
			m.projects = rows
		}
		if rows, err := m.deps.Cache.ModelsTotals(m.modelsWindow); err == nil {
			m.models = rows
		}
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
		case "enter":
			if m.tab == TabHistory || m.tab == TabProjects {
				m.drilled = true
			}
		case "esc":
			m.drilled = false
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
	case TabToday:
		body = renderToday(m.today)
	case TabHistory:
		if m.drilled {
			body = "  History — drill-down (per-project for selected day)\n  [v0.1]"
		} else {
			body = renderHistory(m.history)
		}
	case TabProjects:
		if m.drilled {
			body = "  Projects — drill-down (per-day for selected project)\n  [v0.1]"
		} else {
			body = renderProjects(m.projects, time.Now())
		}
	case TabModels:
		body = renderModels(m.models, m.modelsWindow)
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

func or30(d int) int {
	if d == 0 {
		return 30
	}
	return d
}

// computeWindowFromDeps is a thin shim around status.Compute.
// Lives here to avoid leaking config plumbing into Deps; the TUI
// re-uses the cache.DB() handle and a constant ceiling-label of "max_20x"
// for v0. Phase 11 polish will pull tier from config.
func computeWindowFromDeps(d Deps, now time.Time) (status.Window, error) {
	return status.Compute(d.Cache.DB(), now, "max_20x", 240_000_000)
}
