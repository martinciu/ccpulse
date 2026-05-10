package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/status"
)

// horizontalScrollStep is the per-keypress shift in columns.
const horizontalScrollStep = 3

// RefreshMsg is sent by the watcher loop to trigger a TUI re-query.
type RefreshMsg struct{}

// IndexProgressMsg is sent by the startup backfill goroutine.
type IndexProgressMsg struct {
	Done   int
	Total  int
	Active bool
}

// QuotaMsg is sent when fresh usage data is available.
type QuotaMsg struct {
	Usage     *anthro.Usage
	Source    string
	UpdatedAt time.Time
}

// Deps wires external dependencies into the TUI model.
type Deps struct {
	Cache        *cache.Cache
	ProjectsRoot string
	Credential   anthro.Credential
	HasOAuth     bool
	CacheDir     string
}

// Model is the root Bubble Tea model for the chart view.
type Model struct {
	deps       Deps
	keys       KeyMap
	progress   progress.Model // 5-hour quota bar
	progress7d progress.Model // 7-day quota bar
	viewport   viewport.Model
	help       help.Model
	showHelp   bool

	zoomIdx int // index into ZoomLevels

	window         status.Window
	quota          *anthro.Usage
	quotaSource    string
	quotaUpdatedAt time.Time

	indexActive bool
	indexDone   int
	indexTotal  int

	w, h int
}

func New(d Deps) Model {
	m := Model{
		deps:    d,
		keys:    defaultKeyMap(),
		help:    help.New(),
		zoomIdx: 1, // default: 15m
	}
	m.progress = newProgressBar(0, 40)
	m.progress7d = newProgressBar(0, 40)
	m.viewport = viewport.New(80, 20)
	m.viewport.SetHorizontalStep(horizontalScrollStep)
	return m
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.viewport.Width = m.chartWidth()
		m.viewport.Height = m.chartHeight()
		m.progress = newProgressBar(float64(m.window.Percent)/100.0, m.progressWidth())
		m.progress7d = newProgressBar(float64(m.window.Percent7d)/100.0, m.progressWidth())
		m.refreshChart()
	case IndexProgressMsg:
		m.indexActive = msg.Active
		m.indexDone = msg.Done
		m.indexTotal = msg.Total
	case QuotaMsg:
		m.quota = msg.Usage
		m.quotaSource = msg.Source
		m.quotaUpdatedAt = msg.UpdatedAt
		m.recomputeWindow()
		m.refreshChart()
	case RefreshMsg:
		m.recomputeWindow()
		m.refreshChart()
	case tea.KeyMsg:
		switch {
		case msg.String() == "ctrl+c":
			return m, tea.Quit
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		case key.Matches(msg, m.keys.Zoom):
			m.zoomIdx = (m.zoomIdx + 1) % len(ZoomLevels)
			m.refreshChart()
		case key.Matches(msg, m.keys.ScrollLeft):
			m.viewport.ScrollLeft(horizontalScrollStep)
		case key.Matches(msg, m.keys.ScrollRight):
			m.viewport.ScrollRight(horizontalScrollStep)
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.w == 0 {
		return ""
	}
	header := renderHeader(m.window, m.w, IndexProgress{
		Done: m.indexDone, Total: m.indexTotal, Active: m.indexActive,
	})
	bars := m.quotaBars()
	zoom := ZoomLevels[m.zoomIdx]
	label := lipgloss.NewStyle().Foreground(Base01).Render(
		fmt.Sprintf("  %s per bar  ·  [z] zoom", zoom.Label),
	)
	sep := lipgloss.NewStyle().Foreground(Base02).Render(strings.Repeat("─", m.w))
	var body string
	if m.showHelp {
		body = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		body = m.viewport.View()
	}
	footer := m.help.View(m.keys)
	return lipgloss.JoinVertical(lipgloss.Left, header, bars, label, sep, body, sep, footer)
}

// quotaBars renders the 5h and 7d quota progress bars as a two-row block.
// When 7d data is unavailable the second row shows a placeholder so the
// chart layout below stays stable across the lifecycle of a quota fetch.
func (m Model) quotaBars() string {
	labelStyle := lipgloss.NewStyle().Foreground(Base01).Bold(true)

	bar5h := labelStyle.Render("5h ") +
		m.progress.ViewAs(float64(m.window.Percent)/100.0) +
		fmt.Sprintf(" %3d%%", m.window.Percent)

	var bar7d string
	if m.window.Has7d {
		bar7d = labelStyle.Render("7d ") +
			m.progress7d.ViewAs(float64(m.window.Percent7d)/100.0) +
			fmt.Sprintf(" %3d%%", m.window.Percent7d)
	} else {
		bar7d = labelStyle.Render("7d ") +
			lipgloss.NewStyle().Foreground(Base01).Render("(no data)")
	}

	return lipgloss.NewStyle().Padding(0, 2).Render(
		lipgloss.JoinVertical(lipgloss.Left, bar5h, bar7d),
	)
}

// refreshChart queries the cache and updates the viewport content.
// Safe to call when deps.Cache is nil (no-op).
func (m *Model) refreshChart() {
	if m.deps.Cache == nil {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	since := time.Now().Add(-7 * 24 * time.Hour)
	buckets, err := m.deps.Cache.TokenBuckets(zoom.Duration, since)
	if err != nil || len(buckets) == 0 {
		return
	}
	chartW := len(buckets)
	chartH := m.chartHeight()
	if chartH < 1 {
		chartH = 10
	}
	content := buildChart(buckets, chartW, chartH)
	m.viewport.SetContent(content)
	// Anchor the view at "now" on each refresh — the rightmost column.
	// SetXOffset is clamped internally by the viewport.
	m.viewport.SetXOffset(chartW)
}

// recomputeWindow updates the status.Window from the DB + quota data.
func (m *Model) recomputeWindow() {
	if m.deps.Cache == nil {
		return
	}
	in := status.QuotaInput{
		Usage:      m.quota,
		Source:     m.quotaSource,
		UpdatedAt:  m.quotaUpdatedAt,
		TierSlug:   anthro.TierSlug(m.deps.Credential.RateLimitTier),
		TierPretty: anthro.TierPretty(m.deps.Credential.RateLimitTier),
	}
	if w, err := status.Compute(m.deps.Cache.DB(), time.Now(), in); err == nil {
		m.window = w
	}
	pct := float64(m.window.Percent) / 100.0
	m.progress = newProgressBar(pct, m.progressWidth())
	pct7d := float64(m.window.Percent7d) / 100.0
	m.progress7d = newProgressBar(pct7d, m.progressWidth())
}

// chartWidth returns the available width for the viewport.
func (m Model) chartWidth() int {
	w := m.w - 2
	if w < 10 {
		return 10
	}
	return w
}

// chartHeight returns the available rows for the chart, leaving room for
// header, two-row quota bar block, zoom label, two separators, and footer.
func (m Model) chartHeight() int {
	// Bordered header box = 4 rows. Quota bars 2 (5h + 7d), label 1,
	// top sep 1, bottom sep 1, footer 1. Total non-body overhead = 10.
	h := m.h - 10
	if h < 5 {
		return 5
	}
	return h
}

// progressWidth returns the rendered width of each quota bar, leaving
// room for the surrounding chrome on the bar row:
//   - 4 cols horizontal padding (Padding(0, 2) on the outer block)
//   - 3 cols label prefix ("5h ")
//   - 5 cols percent suffix (" 100%")
func (m Model) progressWidth() int {
	w := m.w - 12
	if w < 10 {
		return 10
	}
	return w
}

// newProgressBar builds a quota bar styled by percent threshold (the bar's
// solid fill is fixed at construction, so the color only changes when this
// is rebuilt). The actual fill amount is supplied at render time via
// progress.ViewAs.
func newProgressBar(pct float64, w int) progress.Model {
	color := Violet
	switch {
	case pct >= 0.90:
		color = Red
	case pct >= 0.70:
		color = Yellow
	}
	if w < 10 {
		w = 10
	}
	return progress.New(
		progress.WithSolidFill(string(color)),
		progress.WithWidth(w),
		progress.WithoutPercentage(),
	)
}
