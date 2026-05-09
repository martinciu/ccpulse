package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/state"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/martinciu/ccpulse/pkg/tmux"
)

// RefreshMsg is sent by the watcher loop to trigger a TUI re-query.
type RefreshMsg struct{}

// IndexProgressMsg is sent by the startup backfill goroutine. The
// header renders an "indexing N/M" suffix while Active is true and
// removes it when the final message (Active:false) lands.
type IndexProgressMsg struct {
	Done   int
	Total  int
	Active bool
}

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
	Cache         *cache.Cache
	ProjectsRoot  string
	HistoryDays   int
	Tier          string
	CeilingTokens int64
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
	liveScope    string
	indexActive  bool
	indexDone    int
	indexTotal   int
}

func New(d Deps) Model {
	st := state.Load()
	scope := st.LiveScope
	if scope == "" {
		scope = "global"
	}
	return Model{
		deps:         d,
		tab:          TabLive,
		style:        DefaultStyle(Violet),
		modelsWindow: cache.WindowToday,
		liveScope:    scope,
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case IndexProgressMsg:
		m.indexActive = msg.Active
		m.indexDone = msg.Done
		m.indexTotal = msg.Total
		return m, nil
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
		case "g":
			if m.tab == TabLive {
				if m.liveScope == "global" {
					m.liveScope = "this_tmux"
				} else {
					m.liveScope = "global"
				}
				_ = state.Save(state.State{LiveScope: m.liveScope, Tab: m.tab.String()})
			}
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
		thisTmux := m.currentTmuxSessionIDs(m.deps.ProjectsRoot)
		rows := m.live
		if m.liveScope == "this_tmux" {
			filtered := rows[:0:0]
			for _, r := range rows {
				if thisTmux[r.SessionID] {
					filtered = append(filtered, r)
				}
			}
			rows = filtered
		}
		body = renderLive(rows, thisTmux, time.Now())
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
	footer := m.style.Footer.Render("[tab]→  [g]scope  [enter]drill  [?]help  [q]")
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
	tier := d.Tier
	if tier == "" {
		tier = "max_20x"
	}
	ceiling := d.CeilingTokens
	if ceiling == 0 && tier != "api" {
		ceiling = 240_000_000 // sensible default if controller didn't set it
	}
	return status.Compute(d.Cache.DB(), now, tier, ceiling)
}

// currentTmuxSessionIDs returns the set of session IDs whose JSONL is
// the most-recently-modified one in a slug whose decoded path matches
// any pane in the current tmux session.
//
// Outside tmux: returns an empty map (no markers applied).
// Errors from tmux calls are swallowed — markers are best-effort.
func (m Model) currentTmuxSessionIDs(projectsRoot string) map[string]bool {
	out := map[string]bool{}
	if os.Getenv("TMUX") == "" {
		return out
	}
	t := tmux.New()
	sess, err := t.CurrentSession()
	if err != nil {
		return out
	}
	paths, err := t.PanePaths(strings.TrimSpace(sess))
	if err != nil {
		return out
	}
	for _, p := range paths {
		slug := encodeSlug(p)
		dir := filepath.Join(projectsRoot, slug)
		jsonl, err := mostRecentJSONL(dir)
		if err != nil || jsonl == "" {
			continue
		}
		sid := strings.TrimSuffix(filepath.Base(jsonl), ".jsonl")
		out[sid] = true
	}
	return out
}

// encodeSlug is the inverse of canonical.DecodeSlug for the `/` and `.`
// substitutions: '/' → '-', '.' → '--'.
func encodeSlug(path string) string {
	s := strings.ReplaceAll(path, ".", "--")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// mostRecentJSONL returns the path to the most recently modified
// `*.jsonl` file in dir, or empty string if none exists.
func mostRecentJSONL(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var newestName string
	var newestT time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, _ := e.Info()
		if newestName == "" || info.ModTime().After(newestT) {
			newestName = e.Name()
			newestT = info.ModTime()
		}
	}
	if newestName == "" {
		return "", nil
	}
	return filepath.Join(dir, newestName), nil
}
