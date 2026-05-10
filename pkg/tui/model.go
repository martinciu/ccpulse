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
		// help.Width controls when ShortHelp ellipsizes; if left at 0
		// the footer can wrap onto the body row and break chartHeight().
		m.help.Width = m.w
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
		return ""
	}
	header := renderHeader(m.w, m.quotaBars())
	sep := lipgloss.NewStyle().Foreground(Base02).Render(strings.Repeat("─", m.w))
	var body string
	if m.showHelp {
		body = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		body = m.viewport.View()
	}
	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, sep, body, sep, footer)
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
// Safe to call when deps.Cache is nil (no-op). The chart always renders
// at the full per-zoom width: an empty cache produces a flat baseline.
func (m *Model) refreshChart() {
	if m.deps.Cache == nil {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	// Right edge = the END of the bucket containing now, so the bucket
	// itself is included in the half-open [from, to) window. Without
	// the +Duration shift the in-flight bucket is silently dropped
	// until the next boundary tick.
	to := cache.BucketAlign(time.Now(), zoom.Duration).Add(zoom.Duration)
	from := to.Add(-zoom.Lookback)
	buckets, err := m.deps.Cache.TokenBuckets(zoom.Duration, from, to)
	if err != nil || len(buckets) == 0 {
		// Defensive: cache error or unaligned bounds. Render an empty
		// gap baseline of the expected width rather than leaving stale
		// viewport content.
		n := int(zoom.Lookback / zoom.Duration)
		empty := make([]cache.TokenBucket, n)
		for i := range empty {
			empty[i] = cache.TokenBucket{BucketStart: from.Add(time.Duration(i) * zoom.Duration)}
		}
		m.viewport.SetContent(buildChart(empty, n, m.chartHeight()))
		m.viewport.SetXOffset(n)
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
