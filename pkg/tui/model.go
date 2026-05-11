package tui

import (
	"fmt"
	"log/slog"
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

// viewLogThreshold gates the slog.Debug emitted from View(); frames
// faster than this aren't logged so idle/animation renders stay quiet
// on the dev channel. 5 ms is below the perception floor.
const viewLogThreshold = 5 * time.Millisecond

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
	IsDev        bool
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
	m.progress = newProgressBar(40)
	m.progress7d = newProgressBar(40)
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
		// help.Width controls when ShortHelp ellipsizes; if left at 0
		// the footer can wrap onto the body row and break chartHeight().
		m.help.Width = m.w
		m.progress = newProgressBar(m.progressWidth())
		m.progress7d = newProgressBar(m.progressWidth())
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
		start := time.Now()
		m.recomputeWindow()
		m.refreshChart()
		slog.Debug("tui.refreshMsg",
			"dur_ms", time.Since(start).Milliseconds(),
			"zoom", ZoomLevels[m.zoomIdx].Label)
	case tea.KeyMsg:
		switch {
		case msg.String() == "ctrl+c":
			return m, tea.Quit
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		case m.showHelp:
			// Suppress chart-affecting keys while the help overlay is up,
			// so dismissing help returns the user to the same scroll/zoom
			// state they left.
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
		return "" // pre-init; don't time
	}
	start := time.Now()
	header := renderHeader(m.w, m.quotaBars())
	sep := lipgloss.NewStyle().Foreground(Base02).Render(strings.Repeat("─", m.w))
	var body string
	if m.showHelp {
		body = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		body = m.viewport.View()
	}
	footer := m.renderFooter()
	out := lipgloss.JoinVertical(lipgloss.Left, header, sep, body, sep, footer)
	if d := time.Since(start); d >= viewLogThreshold {
		slog.Debug("tui.View",
			"dur_ms", d.Milliseconds(),
			"zoom", ZoomLevels[m.zoomIdx].Label,
			"chartW", m.chartWidth(),
			"chartH", m.chartHeight(),
			"show_help", m.showHelp)
	}
	return out
}

// renderFooter composes the bottom line: keybinding help on the left,
// status indicators right-aligned. When no indicators are active, the
// line is just the keybindings. Overflow on narrow terminals truncates
// terminal-side; indicators are transient so the user can widen.
func (m Model) renderFooter() string {
	left := m.help.View(m.keys)
	right := renderIndicators(m.deps.IsDev, IndexProgress{
		Done: m.indexDone, Total: m.indexTotal, Active: m.indexActive,
	}, m.window)
	if right == "" {
		return left
	}
	pad := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// renderIndicators builds the right-aligned status block for the footer.
// Indicators are ordered stale → indexing → [DEV] (dev rightmost), joined
// by dim ' · ' separators, and only included when active. Returns "" when
// nothing's active so the footer is just keybindings.
//
// Styling note: stale-quota uses the default foreground (intentionally
// undimmed — it's a warning meant to draw the eye); indexing and [DEV]
// are dim. The separator is dim.
func renderIndicators(isDev bool, idx IndexProgress, w status.Window) string {
	dim := lipgloss.NewStyle().Foreground(Base01)
	var parts []string
	if w.QuotaSource == "cache_stale" {
		mins := int(time.Since(w.QuotaUpdatedAt).Minutes())
		if mins < 1 {
			mins = 1
		}
		parts = append(parts, fmt.Sprintf("⚠ %dm old", mins))
	}
	if idx.Active {
		parts = append(parts, dim.Render(fmt.Sprintf("indexing %d/%d", idx.Done, idx.Total)))
	}
	if isDev {
		parts = append(parts, dim.Render("[DEV]"))
	}
	if len(parts) == 0 {
		return ""
	}
	sep := dim.Render(" · ")
	return strings.Join(parts, sep)
}

// quotaBars renders the 5h and 7d quota bars as a single line, designed
// to live as the sole content row of the bordered header box. The two
// bars are separated by a dim '│' divider; when 7d data is unavailable
// that side shows a 'no data' placeholder padded to match the live-bar
// slot width so the box right edge stays stable across has-data ↔
// no-data transitions.
func (m Model) quotaBars() string {
	dimStyle := lipgloss.NewStyle().Foreground(Base01)

	left := m.progress.ViewAs(float64(m.window.Percent)/100.0) +
		fmt.Sprintf(" %3d%%  %s", m.window.Percent, durString(m.window.MinutesToReset))

	var right string
	if m.window.Has7d {
		right = m.progress7d.ViewAs(float64(m.window.Percent7d)/100.0) +
			fmt.Sprintf(" %3d%%  %s", m.window.Percent7d, formatReset7d(m.window.MinutesToReset7d))
	} else {
		right = dimStyle.Width(m.progressWidth() + 12).Render("(no data)")
	}

	divider := dimStyle.Render(" │ ")
	return left + divider + right
}

// refreshChart queries the cache and updates the viewport content.
// Safe to call when deps.Cache is nil (no-op). Loads the full history
// present in the cache, from the earliest message up to "now". On an
// empty cache or a DB error, renders a placeholder.
func (m *Model) refreshChart() {
	if m.deps.Cache == nil {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	// Right edge = the END of the bucket containing now, so the bucket
	// itself is included in the half-open [from, to) window.
	to := cache.BucketAlign(time.Now(), zoom.Duration).Add(zoom.Duration)

	earliest, ok, err := m.deps.Cache.EarliestMessageTime()
	if err != nil || !ok {
		m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
		m.viewport.SetXOffset(0)
		return
	}

	from := cache.BucketAlign(earliest, zoom.Duration)
	buckets, err := m.deps.Cache.TokenBuckets(zoom.Duration, from, to)
	if err != nil || len(buckets) == 0 {
		m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
		m.viewport.SetXOffset(0)
		return
	}

	chartW := len(buckets)
	chartH := m.chartHeight()
	if chartH < 1 {
		chartH = 10
	}
	m.viewport.SetContent(buildChart(buckets, chartW, chartH))
	// Anchor the view at "now" on each refresh — the rightmost column.
	m.viewport.SetXOffset(chartW)
}

// emptyPlaceholder returns content sized w×h showing a centered
// "no Claude sessions yet" line styled in the dim Base01 colour.
// lipgloss.Place pads the surrounding rows with spaces so a previous
// non-empty refresh's content is fully wiped from the viewport.
func emptyPlaceholder(w, h int) string {
	if h < 1 {
		h = 1
	}
	if w < 1 {
		w = 1
	}
	msg := lipgloss.NewStyle().Foreground(Base01).Render("no Claude sessions yet")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, msg)
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
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())
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
// the bordered header box (3 rows: top border, bars row, bottom border),
// two separators (2 rows), and the help footer (1 row). Total non-body
// overhead = 6 rows.
func (m Model) chartHeight() int {
	h := m.h - 6
	if h < 5 {
		return 5
	}
	return h
}

// progressWidth returns the rendered width of each of the two quota bars,
// which sit side-by-side inside the header box. Per-side chrome:
//   - 5 cols percent suffix (" 100%")
//   - 5h reset slot: up to 8 cols ("  4h 59m" via durString)
//   - 7d reset slot: up to 7 cols ("  23:59" via formatReset7d, or "  Xd")
//
// The header box itself reserves 4 cols (border + padding), and a 3-col
// '│' divider sits between the two halves. So total chrome = 4 + 3 +
// (5+8) + (5+7) = 32, split across two bars.
func (m Model) progressWidth() int {
	w := (m.w - 32) / 2
	if w < 6 {
		return 6
	}
	return w
}

// newProgressBar builds a quota bar with the bubbles/progress default
// gradient (#5A56E0 → #EE6FF8). The actual fill amount is supplied at
// render time via progress.ViewAs.
func newProgressBar(w int) progress.Model {
	if w < 10 {
		w = 10
	}
	return progress.New(
		progress.WithWidth(w),
		progress.WithoutPercentage(),
		progress.WithDefaultGradient(),
	)
}
