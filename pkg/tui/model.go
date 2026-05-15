package tui

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
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

// tickFadeMsg drives the post-backfill checkmark fade. Scheduled by
// the IndexProgressMsg handler on the Active: true → false falling
// edge, and re-scheduled by the tickFadeMsg handler until
// indexFadeStop exceeds indexFadeStopCount. Idle TUI cost after the
// fade ends is zero — no Cmd is returned at the final stop.
type tickFadeMsg struct{}

// springTickMsg drives the per-bar harmonica spring loop after a
// 'u' unit-toggle. Scheduled by Update on the unit-key path and
// re-scheduled by the springTickMsg handler until all springs are
// settled, after which idle TUI cost returns to zero (no further
// Cmd is returned).
type springTickMsg struct{}

// springPhase tracks which leg of the two-phase unit-toggle animation
// is currently running. Idle is the steady state; springActive=false
// implies springPhase=springIdle. See issue #136.
type springPhase int

const (
	springIdle      springPhase = iota
	springShrinking             // Phase 1: bars fall to zero (Projectile, ease-in)
	springHolding               // Hold: bars rest at zero so the eye registers the beat (#163)
	springGrowing               // Phase 2: bars grow from zero to target (Spring with Vi, ease-out)
)

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
	unitIdx int // 0 = tokens, 1 = cost. Cycled by 'u'. Resets on launch.

	// lastValues / lastStarts are the per-bucket inputs fed to the
	// most recent buildChart, in the active unit. Refreshed by
	// refreshChart; lastValues is snapshotted by beginUnitAnimation
	// as the spring's initial state, and lastStarts feeds the
	// per-tick animated chart rebuild without re-querying the cache.
	lastValues []float64
	lastStarts []time.Time

	// lastPts5h / lastPts7d hold the raw utilization points for the
	// remaining-quota line chart. Populated by refreshChart when
	// unitIdx == chartUnitRemaining; nil otherwise. Used by
	// renderSpringFrame for the line-mode animation path.
	lastPts5h []cache.UtilizationPoint
	lastPts7d []cache.UtilizationPoint

	// oldPts5h / oldPts7d are snapshotted in beginUnitAnimation so
	// renderSpringFrame can render the exiting line chart (oldIsLine=true
	// phase) after refreshChart has already overwritten lastPts5h/7d
	// with the new unit's data.
	oldPts5h []cache.UtilizationPoint
	oldPts7d []cache.UtilizationPoint

	// lastChartFrom / lastChartTo / lastCanvasW are the [from, to) time
	// window and column-count used by the most recent buildChart /
	// buildLineChart call (any unit). Stored so refreshChart can map
	// the viewport column back to a wall-clock anchor on the NEXT
	// refresh (zoom, unit toggle, watcher event), and so
	// renderSpringFrame can reproduce the same x-axis during animation.
	lastChartFrom time.Time
	lastChartTo   time.Time
	lastCanvasW   int
	// lastZoomStride is the per-bar column distance captured at the
	// same refreshChart pass as lastCanvasW. Used to derive the
	// PREVIOUS viewport column offset (= viewportXOffset *
	// lastZoomStride) when the user just pressed 'z' and the current
	// ZoomLevels[m.zoomIdx].stride() reflects the NEW zoom, not the
	// one the viewport was last drawn against.
	lastZoomStride int

	// viewportXOffset shadows m.viewport's unexported xOffset. We need a
	// readable scroll position to preserve the wall-clock anchor across
	// refreshes; v1 viewport only exposes a setter. Maintained by
	// setX/scrollLeft/scrollRight wrappers; every viewport scroll mutation
	// goes through them, including in tests — bypassing the wrappers makes
	// the shadow stale and breaks wall-clock preservation on the next
	// refresh.
	viewportXOffset int

	// Animation state (per-bar harmonica spring) for the 'u' unit toggle.
	// Spring values live in [0, 1] ratio space (each bar's ratio of the
	// active-unit's peak), not raw units — the two units differ by orders
	// of magnitude so raw-value springs would render bars at catastrophic
	// heights. springActive is the gate: idle TUI cost is zero when false.
	springs            []harmonica.Spring
	springRatios       []float64
	springVelocities   []float64
	springTargetRatios []float64
	springActive       bool
	// springXOffset is the leftmost bucket index visible in the viewport
	// when animation started. The spring runs over all bucket ratios but
	// only the visible window is re-rendered each tick — full-canvas
	// rebuilds at chart widths > 1000 buckets exceed the 60fps budget
	// (BenchmarkBarChartRender).
	springXOffset int
	// Two-phase animation state (issue #136). springProjectiles drives
	// Phase 1 (Projectile, per bar, per-bar tuned gravity). springs
	// drives Phase 2 (Spring with seeded initial velocity). Final
	// targets are held in springFinalTargets during Phase 1 because
	// springTargetRatios is zeros while bars fall.
	springPhase        springPhase
	springProjectiles  []harmonica.Projectile
	springFinalTargets []float64
	// oldPeak / oldUnitIdx are snapshotted in beginUnitAnimation BEFORE
	// refreshChart switches m.peak / m.unitIdx to the new unit. View()
	// uses them during Phase 1 so the fading Y-label shows the OLD
	// unit's value at the OLD peak.
	oldPeak    float64
	oldUnitIdx int
	// oldIsLine / newIsLine track whether exit/enter render as line charts.
	// Set by beginUnitAnimation; read by renderSpringFrame and View.
	oldIsLine bool
	newIsLine bool

	window         status.Window
	quota          *anthro.Usage
	quotaSource    string
	quotaUpdatedAt time.Time

	indexActive bool
	indexDone   int
	indexTotal  int

	// Fade state. indexLastActive is the edge detector; indexFadeStop
	// is 0 when idle, 1–3 during the post-backfill fade window. See
	// indexFadeStyle and indexFadeStepDuration in style.go.
	indexLastActive bool
	indexFadeStop   int

	// Cached on refreshChart so View() doesn't re-iterate buckets per
	// frame. peak is the max bucket value in the current chart range;
	// drives the Y label column rendered outside the scrollable viewport.
	peak float64

	w, h int

	// dateOrder is detected once at New() from LC_TIME / LC_ALL / LANG
	// and never mutated — locale doesn't change at runtime. Threaded
	// through buildChart → renderXLabels → formatXLabel → dateLabel
	// to drive the day-boundary stamp's MM/DD vs DD/MM order.
	dateOrder dateOrder
}

func New(d Deps) Model {
	m := Model{
		deps:      d,
		keys:      defaultKeyMap(),
		help:      help.New(),
		zoomIdx:   0, // default: 15m
		dateOrder: detectDateOrder(),
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
		wasActive := m.indexLastActive
		m.indexActive = msg.Active
		m.indexDone = msg.Done
		m.indexTotal = msg.Total
		m.indexLastActive = msg.Active
		switch {
		case msg.Active:
			// Defensive — clears any in-flight fade if a second
			// backfill ever re-enters the active state. Unreachable
			// in current code (Backfill.Run is one-shot).
			m.indexFadeStop = 0
		case wasActive && !msg.Active:
			// Falling edge — start the fade.
			m.indexFadeStop = 1
			return m, tea.Tick(indexFadeStepDuration, func(time.Time) tea.Msg {
				return tickFadeMsg{}
			})
		}
	case tickFadeMsg:
		if m.indexFadeStop == 0 {
			// Stale tick — no fade in progress. Drop silently.
			return m, nil
		}
		m.indexFadeStop++
		if m.indexFadeStop > indexFadeStopCount {
			m.indexFadeStop = 0
			return m, nil
		}
		return m, tea.Tick(indexFadeStepDuration, func(time.Time) tea.Msg {
			return tickFadeMsg{}
		})
	case springTickMsg:
		if !m.springActive {
			return m, nil
		}
		switch m.springPhase {
		case springShrinking:
			var maxR float64
			for i := range m.springRatios {
				pos := m.springProjectiles[i].Update()
				// Defensive clamp — early-exit beats us to it under
				// well-tuned per-bar gravity, but Projectile keeps
				// accelerating past zero if we let it.
				pos.X = max(pos.X, 0)
				m.springRatios[i] = pos.X
				maxR = max(maxR, pos.X)
			}
			if maxR < phaseTransitionThreshold {
				// Phase 1 → Hold handoff: snap ratios to zero, render the
				// all-zero frame once, schedule a one-shot tick at
				// phaseHoldDuration. Phase 2 state (springTargetRatios,
				// springVelocities) is seeded in the springHolding case
				// when the hold tick arrives — not here (#163).
				for i := range m.springRatios {
					m.springRatios[i] = 0
				}
				m.springPhase = springHolding
				m.renderSpringFrame()
				return m, tea.Tick(phaseHoldDuration, func(time.Time) tea.Msg {
					return springTickMsg{}
				})
			}
			m.renderSpringFrame()
			return m, tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
				return springTickMsg{}
			})

		case springHolding:
			// Hold tick arrived: seed Phase 2 targets and initial
			// velocities, switch to springGrowing, resume FPS ticking.
			// Ratios remain at zero (already snapped in the Phase 1
			// threshold-cross). The first springGrowing tick will move
			// them off zero (#163).
			for i := range m.springRatios {
				m.springTargetRatios[i] = m.springFinalTargets[i]
				m.springVelocities[i] = phase2InitialVelocityV0 * m.springFinalTargets[i]
			}
			m.springPhase = springGrowing
			m.renderSpringFrame()
			return m, tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
				return springTickMsg{}
			})

		case springGrowing:
			var maxGap float64
			for i := range m.springRatios {
				r, v := m.springs[i].Update(m.springRatios[i],
					m.springVelocities[i], m.springTargetRatios[i])
				m.springRatios[i] = r
				m.springVelocities[i] = v
				gap := math.Abs(m.springTargetRatios[i] - r)
				maxGap = max(maxGap, gap)
			}
			if maxGap < phaseTransitionThreshold {
				copy(m.springRatios, m.springTargetRatios)
				m.springActive = false
				m.springPhase = springIdle
				m.refreshChart()
				return m, nil
			}
			m.renderSpringFrame()
			return m, tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
				return springTickMsg{}
			})
		}
		return m, nil
	case QuotaMsg:
		m.quota = msg.Usage
		m.quotaSource = msg.Source
		m.quotaUpdatedAt = msg.UpdatedAt
		m.recomputeWindow()
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
		case key.Matches(msg, m.keys.Unit):
			m.unitIdx = (m.unitIdx + 1) % int(chartUnitCount)
			m.beginUnitAnimation()
			if m.springActive {
				// After beginUnitAnimation, viewport content is the new
				// full-canvas with XOffset preserved at the user's
				// wall-clock anchor (via refreshChart). Use the shadow
				// scroll position as the spring's window so the animated
				// slice matches what the user is actually looking at.
				m.springXOffset = m.viewportXOffset
				// Paint spring-frame-0 (old heights, old unit, old color)
				// synchronously so the next View() call doesn't show one
				// frame of refreshChart's new-unit content before the
				// first tick paints the falling old-unit chart.
				m.renderSpringFrame()
				return m, tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
					return springTickMsg{}
				})
			}
		case key.Matches(msg, m.keys.ScrollLeft):
			m.scrollLeft(horizontalScrollStep)
		case key.Matches(msg, m.keys.ScrollRight):
			m.scrollRight(horizontalScrollStep)
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
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", m.w))
	var body string
	if m.showHelp {
		body = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		fade := 1.0
		labelUnit := chartUnit(m.unitIdx)
		labelPeak := m.peak
		rawBody := m.viewport.View()
		if m.springActive {
			var maxR float64
			for _, r := range m.springRatios {
				maxR = max(maxR, r)
			}
			fade = maxR
			// Determine whether the current rendered frame is a line chart.
			// Exit phase shows OLD type; hold/enter shows NEW type.
			renderingLine := false
			switch m.springPhase {
			case springShrinking:
				renderingLine = m.oldIsLine
				if !renderingLine {
					labelUnit = chartUnit(m.oldUnitIdx)
					labelPeak = m.oldPeak
				}
			default: // springHolding, springGrowing
				renderingLine = m.newIsLine
			}
			if renderingLine {
				body = overlayYTicks(rawBody, m.chartHeight(), fade)
			} else {
				body = overlayYLabel(rawBody, labelPeak, labelUnit, m.chartHeight(), fade)
			}
		} else if chartUnit(m.unitIdx) == chartUnitRemaining {
			body = overlayYTicks(rawBody, m.chartHeight(), 1.0)
		} else {
			body = overlayYLabel(rawBody, m.peak, chartUnit(m.unitIdx), m.chartHeight(), 1.0)
		}
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
//
// avail floors at W(right)+1 so the right block always gets at least a
// 1-col gutter even when the help line is too wide to leave room — this
// preserves the existing overflow-and-truncate behaviour.
func (m Model) renderFooter() string {
	left := m.help.View(m.keys)
	right := renderIndicators(m.deps.IsDev, IndexProgress{
		Done:     m.indexDone,
		Total:    m.indexTotal,
		Active:   m.indexActive,
		FadeStop: m.indexFadeStop,
	}, m.window)
	if right == "" {
		return left
	}
	avail := max(m.w-lipgloss.Width(left), lipgloss.Width(right)+1)
	return left + lipgloss.PlaceHorizontal(avail, lipgloss.Right, right)
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
	var parts []string
	if w.QuotaSource == "cache_stale" {
		mins := max(int(time.Since(w.QuotaUpdatedAt).Minutes()), 1)
		parts = append(parts, fmt.Sprintf("⚠ %dm old", mins))
	}
	switch {
	case idx.Active:
		parts = append(parts, dimStyle.Render(fmt.Sprintf("indexing %d/%d", idx.Done, idx.Total)))
	case idx.FadeStop > 0:
		parts = append(parts, indexFadeStyle(idx.FadeStop).Render(fmt.Sprintf("✓ indexed %d", idx.Done)))
	}
	if isDev {
		parts = append(parts, dimStyle.Render("[DEV]"))
	}
	if len(parts) == 0 {
		return ""
	}
	sep := dimStyle.Render(" · ")
	return strings.Join(parts, sep)
}

// quotaBars renders the two content rows that live inside the bordered
// header box: the existing 5h / 7d quota bars row and the new burn-rate
// row beneath it. Both rows are separated by a dim " │ " divider and
// use symmetric chrome so the divider sits at the true midpoint. When
// 7d data is unavailable that side shows a dim "(no data)" placeholder
// padded to match the live-bar slot width so the box right edge stays
// stable across has-data ↔ no-data transitions.
//
// The burn-rate row pulls projection data from the same status.Window
// the bars row uses — no separate compute path.
func (m Model) quotaBars() string {
	left := renderQuotaSide(
		"5h ",
		m.progress,
		float64(m.window.Percent)/100.0,
		durString(m.window.MinutesToReset),
	)
	// Derive the per-side slot from the actual rendered bars-row left,
	// not from a theoretical formula: newProgressBar clamps to a 10-col
	// minimum even when progressWidth() returns less, so the theoretical
	// per-side width drifts from the rendered width at narrow terminals
	// (the "clamp regime"). Reading lipgloss.Width(left) guarantees the
	// burn-rate row's slots line up with the bars row regardless of clamp.
	slotW := lipgloss.Width(left)

	var right string
	if m.window.Has7d {
		right = renderQuotaSide(
			"7d ",
			m.progress7d,
			float64(m.window.Percent7d)/100.0,
			formatReset7d(m.window.MinutesToReset7d),
		)
	} else {
		// The "(no data)" placeholder genuinely needs a fixed slot so the
		// box right edge stays stable when 7d toggles between data ↔ no-data.
		right = dimStyle.Width(slotW).Render("(no data)")
	}

	divider := dimStyle.Render(" │ ")
	barsRow := lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right)

	// Burn-rate row mirrors the bars layout: same per-side slotW, same
	// divider. The "5h "/"7d " label slot is replaced with same-width
	// blank padding so the burn-rate text starts at the same column as
	// the progress bar above — spatial association already identifies
	// each side; repeating the labels would be redundant.
	// Both projection pointers can be nil (no quota loaded yet, or 7d
	// not exposed by the server) — the side renderer handles that by
	// emitting a dim "(no data)" placeholder.
	var fiveHourProj, sevenDayProj *status.Projection
	if m.window.Projection != nil {
		fiveHourProj = m.window.Projection.FiveHour
		sevenDayProj = m.window.Projection.SevenDay
	}
	burnLeft := renderBurnRateSide(burnPad, fiveHourProj, slotW, 5*time.Hour, burnRateUnitPerHour)
	burnRight := renderBurnRateSide(burnPad, sevenDayProj, slotW, 7*24*time.Hour, burnRateUnitPerDay)
	burnRow := lipgloss.JoinHorizontal(lipgloss.Top, burnLeft, divider, burnRight)

	return lipgloss.JoinVertical(lipgloss.Left, barsRow, burnRow)
}

// springFPS is the harmonica tick rate for the unit-toggle animation.
// 60 leaves a 16.7ms per-frame budget. Task 10 (the bench-gate commit)
// validates the choice against BenchmarkBarChartRender.
const springFPS = 60

// springFrequency, springDamping are the harmonica parameters for the
// per-bar spring. Frequency 6 Hz controls the speed of approach;
// damping 1.0 is critically damped — the spring reaches the target
// as fast as possible WITHOUT overshooting. No bounce, no oscillation,
// just a tight monotonic ease into the new ratios.
const (
	springFrequency = 6.0
	springDamping   = 1.0
)

// Phase durations and tuning for the two-phase animation (#136).
//
// phase1Duration is the wall-clock target for Phase 1 (Projectile fall).
// Per-bar gravity is tuned at beginUnitAnimation so each bar reaches
// zero at exactly t = phase1Duration regardless of starting height.
//
// phase2Frequency, phase2Damping mirror the existing springFrequency
// and springDamping for Phase 2's spring. Critical damping (1.0) +
// initial velocity below omega guarantees monotonic ease-out without
// overshoot.
//
// phase2InitialVelocityV0 is V₀ in Vi[i] = V₀ · springFinalTargets[i].
// Picked below omega=6 so the spring approaches the target monotonically.
//
// phaseTransitionThreshold is the symmetric early-exit cutoff: Phase 1
// hands off when max(springRatios) < threshold; Phase 2 settles when
// max(springTargetRatios − springRatios) < threshold. 0.01 is below
// single-cell visual quantisation.
const (
	phase1Duration           = 350 * time.Millisecond
	phase2Frequency          = springFrequency // 6.0
	phase2Damping            = springDamping   // 1.0
	phase2InitialVelocityV0  = 5.0
	phaseTransitionThreshold = 0.01
	// phaseHoldDuration is the all-zero pause between Phase 1 (fall) and
	// Phase 2 (grow) in the 'u' unit-toggle animation. Long enough to
	// read the unit change, short enough to feel snappy (#163).
	phaseHoldDuration = 150 * time.Millisecond
)

// beginUnitAnimation primes the two-phase unit-toggle animation. It
// snapshots the OLD state (oldPeak, oldUnitIdx, oldValues from
// m.lastValues), runs refreshChart so the viewport content reflects
// the NEW unit, then builds:
//   - springRatios[i] from the OLD ratios (current visible heights).
//   - springFinalTargets[i] from the NEW ratios (Phase 2 destination).
//   - springProjectiles[i] with per-bar tuned gravity so bar i lands
//     at zero at t = phase1Duration regardless of its starting ratio.
//   - springs[i] (Phase 2 spring) configured; springVelocities seeded
//     at the phase transition, not here.
//   - springPhase = springShrinking, springActive = true.
//
// Caller must have already incremented m.unitIdx before calling.
// oldUnitIdx is derived by inverting the toggle since this is a
// 2-cycle (tokens ↔ cost).
//
// Snapshots happen BEFORE refreshChart so the OLD m.peak / m.lastValues
// survive the refresh that overwrites them.
func (m *Model) beginUnitAnimation() {
	if m.deps.Cache == nil {
		return
	}

	oldValues := m.lastValues
	m.oldPeak = m.peak
	m.oldUnitIdx = (m.unitIdx + int(chartUnitCount) - 1) % int(chartUnitCount)
	m.oldIsLine = isLineMode(chartUnit(m.oldUnitIdx))
	m.newIsLine = isLineMode(chartUnit(m.unitIdx))
	// Snapshot pts before refreshChart overwrites them.
	m.oldPts5h = m.lastPts5h
	m.oldPts7d = m.lastPts7d

	m.refreshChart()
	newValues := m.lastValues
	newPeak := m.peak

	if len(newValues) == 0 {
		m.springActive = false
		m.springPhase = springIdle
		return
	}

	n := len(newValues)
	m.springs = make([]harmonica.Spring, n)
	m.springProjectiles = make([]harmonica.Projectile, n)
	m.springRatios = make([]float64, n)
	m.springVelocities = make([]float64, n)
	m.springTargetRatios = make([]float64, n) // zeros — Phase 1 target
	m.springFinalTargets = make([]float64, n)

	t1 := phase1Duration.Seconds()
	for i := range n {
		// Phase 1 exit ratio: line mode collapses as a uniform shape fraction
		// (1.0 = full, 0.0 = flat); bar mode uses the per-bar height ratio.
		if m.oldIsLine {
			m.springRatios[i] = 1.0
		} else if m.oldPeak > 0 && i < len(oldValues) {
			m.springRatios[i] = oldValues[i] / m.oldPeak
		}

		// Phase 2 enter target: line mode springs to shape-fraction 1.0
		// (the interpPt formula then maps 1→real shape); bar mode uses the
		// normalised new value.
		if m.newIsLine {
			m.springFinalTargets[i] = 1.0
		} else if newPeak > 0 {
			m.springFinalTargets[i] = newValues[i] / newPeak
		}

		// Per-bar tuned gravity (quadratic ease-in) so bar/line i hits 0 at
		// t = phase1Duration. h = 0.5·g·t² ⇒ g = 2h/t². Direction is always
		// toward zero (exit target for both bar and line is 0 in ratio-space).
		g := 2 * m.springRatios[i] / (t1 * t1)
		// Stored by value; Phase 1 tick MUST index (m.springProjectiles[i].Update()),
		// never range-copy. Projectile.Update has a pointer receiver and mutates
		// state; a range-copy loop would freeze Phase 1 silently.
		m.springProjectiles[i] = *harmonica.NewProjectile(
			harmonica.FPS(springFPS),
			harmonica.Point{X: m.springRatios[i]},
			harmonica.Vector{},      // v0 = 0 (at rest)
			harmonica.Vector{X: -g}, // accel toward zero
		)
		m.springs[i] = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	}
	m.springActive = true
	m.springPhase = springShrinking
}

// renderSpringFrame rebuilds the viewport content from the visible
// window of spring ratios. Pass 1.0 as the chart's max value because
// the ratios already live in [0, 1]; ntcharts then renders each bar
// at the right proportional height.
//
// PERF: full-canvas rebuilds at chartW > 1000 buckets exceed the 60fps
// per-frame budget (see BenchmarkBarChartRender). The spring runs over
// ALL ratios so every bar settles correctly, but only the chartWidth()
// window starting at m.springXOffset is rendered each tick. Off-screen
// bars are invisible anyway; their final positions are committed on
// settle via the steady-state refreshChart call.
//
// Sets viewport.XOffset = 0 because the windowed canvas is rendered
// starting at slice col 0; the leadingPad below shifts content to
// match the pre-spring viewport position.
func (m *Model) renderSpringFrame() {
	if len(m.springRatios) == 0 {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	chartH := m.chartHeight()

	// Determine whether the current frame renders as a line chart (remaining
	// mode). Exit phase shows the OLD chart type; enter phase shows the NEW.
	renderAsLine := false
	switch m.springPhase {
	case springShrinking:
		renderAsLine = m.oldIsLine
	default:
		renderAsLine = m.newIsLine
	}

	if renderAsLine {
		// Shape-fraction convention: springRatios[i] is a uniform scalar in
		// [0,1] where 1.0 = full real shape and 0 = flat line at 100% headroom.
		// Both exit and enter use the SAME ratio direction (springShrinking:
		// 1→0, springGrowing: 0→1). The visual direction is produced by
		// interpPt below, which maps the scalar onto the displayed value via
		// displayed = 1.0 + (target-1.0)*maxR — so maxR=0 renders flat 100%
		// and maxR=1 renders the real shape. This is why exit (line
		// collapses upward to 100%) and enter (line drops from 100% to real
		// shape) both use ratios approaching 1.0 → 0 and 0 → 1.0 respectively
		// without needing separate direction flags.
		// maxR is the global envelope; all ratios move together.
		var maxR float64
		for _, r := range m.springRatios {
			maxR = max(maxR, r)
		}

		// Select which pts to interpolate: old data during exit, new during enter.
		pts5h, pts7d := m.lastPts5h, m.lastPts7d
		if m.springPhase == springShrinking && m.oldIsLine {
			pts5h, pts7d = m.oldPts5h, m.oldPts7d
		}

		interpPt := func(p cache.UtilizationPoint) cache.UtilizationPoint {
			target := max(0, 1.0-p.Pct/100.0)
			// displayed ∈ [1.0, target]: 1.0 (flat) when maxR=0, target (real) when maxR=1.
			displayed := 1.0 + (target-1.0)*maxR
			return cache.UtilizationPoint{At: p.At, Pct: (1.0 - displayed) * 100.0}
		}

		interp5h := make([]cache.UtilizationPoint, len(pts5h))
		for i, p := range pts5h {
			interp5h[i] = interpPt(p)
		}
		interp7d := make([]cache.UtilizationPoint, len(pts7d))
		for i, p := range pts7d {
			interp7d[i] = interpPt(p)
		}

		from, to := m.lastChartFrom, m.lastChartTo
		if from.IsZero() {
			from = time.Now().Add(-5 * time.Hour)
		}
		if to.IsZero() {
			to = time.Now()
		}
		chartW := m.chartWidth()
		m.viewport.SetContent(buildLineChart(interp5h, interp7d, from, to, chartW, chartH, time.Now(), zoom, m.dateOrder))
		m.viewport.SetXOffset(0)
		return
	}

	nv := m.visibleBuckets()

	// Clamp the window to the actual ratios slice.
	start := m.springXOffset
	if start < 0 {
		start = 0
	}
	end := start + nv
	if end > len(m.springRatios) {
		end = len(m.springRatios)
	}
	if start >= end {
		return
	}

	// Pre-spring viewport.SetXOffset(K*stride) gets clamped to
	// longestLineWidth-Width at the right edge whenever K*stride exceeds
	// the canvas right edge. When the canvas right edge doesn't sit on a
	// stride boundary (24h has BarGap=2 and viewport width is rarely a
	// multiple of 12), the leading slack lands either in the gap before
	// bucket K (visible as 1–2 blank cols) or mid-way through bucket K-1
	// (visible as a partial bar at the left). Either way, the windowed
	// spring canvas must reproduce that leading content so the user
	// doesn't see a "jump left" or a vanishing partial bar on the
	// steady-state ↔ spring transition. Include bucket [start-1] as a
	// leading bar and offset into it by the same slack the viewport
	// would have shown pre-spring.
	stride := zoom.stride()
	prevLongest := zoom.CanvasWidth(len(m.lastValues))
	sliceStart, springXOff := computeSpringSlice(start, prevLongest, m.viewport.Width, stride)

	visibleRatios := m.springRatios[sliceStart:end]
	visibleStarts := m.lastStarts[sliceStart:end]
	// During the shrinking phase the bars are still showing OLD-unit data
	// falling toward zero, so render them in the OLD unit's color. Only
	// after the handoff (springGrowing onward) does the color switch to
	// the new unit. Mirrors the labelUnit logic in Model.View().
	frameUnit := chartUnit(m.unitIdx)
	if m.springPhase == springShrinking {
		frameUnit = chartUnit(m.oldUnitIdx)
	}
	m.viewport.SetContent(buildChart(visibleRatios, visibleStarts, 1.0,
		zoom.CanvasWidth(len(visibleRatios)), chartH, time.Now(), zoom, frameUnit, m.dateOrder))
	m.viewport.SetXOffset(springXOff)
}

// setX is the single point of entry for changing the viewport's horizontal
// scroll position. n is a bucket index (not a column count); setX clamps
// it then multiplies by the per-bar stride (BarWidth+BarGap, defensively
// clamped to ≥1) when delegating to viewport.SetXOffset (column-indexed).
// The shadow viewportXOffset stays in bucket-index space.
//
// Clamp is mode-aware:
//
//   - Bar modes (tokens/cost): clamp against len(lastStarts) -
//     visibleBuckets(). Preserves the existing setX semantics that
//     renderSpringFrame's slack-handling computeSpringSlice was tuned
//     against. The bucket-aligned canvas guarantees lastStarts and
//     visibleBuckets line up.
//   - Remaining mode: clamp against (lastCanvasW - viewport.Width) /
//     stride. lastStarts in remaining mode is sparse sample points
//     (not bucket-aligned), so the bar-mode clamp would collapse to
//     0 the moment usage_samples count drops below visibleBuckets.
//     The canvas-width clamp matches the column-based anchor logic
//     in refreshChart.
func (m *Model) setX(n int) {
	stride := ZoomLevels[m.zoomIdx].stride()
	var maxX int
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		maxX = max(0, m.lastCanvasW-m.viewport.Width) / stride
	} else {
		maxX = max(0, len(m.lastStarts)-m.visibleBuckets())
	}
	n = min(max(n, 0), maxX)
	m.viewport.SetXOffset(n * stride)
	m.viewportXOffset = n
}

func (m *Model) scrollLeft(n int)  { m.setX(m.viewportXOffset - n) }
func (m *Model) scrollRight(n int) { m.setX(m.viewportXOffset + n) }

// refreshChart queries the cache and updates the viewport content.
// Safe to call when deps.Cache is nil (no-op). Loads the full history
// present in the cache, from the earliest message up to "now". On an
// empty cache or a DB error, renders a placeholder.
func (m *Model) refreshChart() {
	if m.deps.Cache == nil {
		return
	}
	// If a unit-toggle spring is still in flight, hard-cut it: refresh
	// paths (watcher RefreshMsg, WindowSizeMsg, Zoom) bypass the
	// animation per the spec — only the initial 'u' press animates.
	// No need to snap springRatios to targets here: the rest of
	// refreshChart overwrites lastValues/lastStarts/peak and rebuilds
	// the viewport from cache; nothing reads springRatios while
	// springActive is false.
	if m.springActive {
		m.springActive = false
		m.springPhase = springIdle
		// springProjectiles, springFinalTargets, oldPeak, oldUnitIdx
		// remain populated but unread — guarded by springActive=false.
		// Next beginUnitAnimation re-makes the slices.
	}

	// Snapshot the wall-clock anchor BEFORE rebuild. The previous canvas
	// state is captured by lastCanvasW + lastChartFrom/To + lastZoomStride
	// at the end of the previous refreshChart pass. The viewport's
	// xOffset is unexported in bubbles v1, so we derive its column
	// position as viewportXOffset * lastZoomStride — the stride at the
	// time the viewport was last drawn (which can differ from the
	// current zoom's stride if the user just pressed 'z').
	//
	// wasPinned == true when the viewport was at the right edge before
	// this refresh; the post-rebuild restore re-pins to the new right
	// edge regardless of canvas width. hadAnchor == false on first load
	// and after empty-cache early-returns; the restore pins to the new
	// right edge in that case too.
	var (
		anchorTime time.Time
		hadAnchor  bool
		wasPinned  bool
	)
	if m.lastCanvasW > 0 && m.lastZoomStride > 0 && !m.lastChartFrom.IsZero() && m.lastChartTo.After(m.lastChartFrom) {
		prevColOffset := m.viewportXOffset * m.lastZoomStride
		prevMaxCol := max(0, m.lastCanvasW-m.viewport.Width)
		wasPinned = prevColOffset >= prevMaxCol
		if !wasPinned {
			anchorTime = columnToTime(prevColOffset, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
			hadAnchor = true
		}
	}

	zoom := ZoomLevels[m.zoomIdx]
	// Right edge = the END of the bucket containing now, so the bucket
	// itself is included in the half-open [from, to) window.
	// 24h zoom uses local-midnight boundaries (DST-correct via AddDate).
	var to time.Time
	if zoom.Duration == 24*time.Hour {
		to = cache.DayStartLocal(time.Now()).AddDate(0, 0, 1)
	} else {
		to = cache.BucketAlign(time.Now(), zoom.Duration).Add(zoom.Duration)
	}

	earliest, ok, err := m.deps.Cache.EarliestMessageTime()
	if err != nil || !ok {
		m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
		m.lastValues = nil
		m.lastStarts = nil
		m.peak = 0
		m.setX(0)
		return
	}

	var from time.Time
	if zoom.Duration == 24*time.Hour {
		from = cache.DayStartLocal(earliest)
	} else {
		from = cache.BucketAlign(earliest, zoom.Duration)
	}

	var (
		values []float64
		starts []time.Time
		peak   float64
		unit   chartUnit
	)
	switch m.unitIdx {
	case int(chartUnitCost): // cost
		buckets, err := m.deps.Cache.CostBuckets(zoom.Duration, from, to)
		if err != nil || len(buckets) == 0 {
			m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
			m.lastValues = nil
			m.lastStarts = nil
			m.peak = 0
			m.setX(0)
			return
		}
		values = make([]float64, len(buckets))
		starts = make([]time.Time, len(buckets))
		for i, b := range buckets {
			values[i] = b.Cost
			starts[i] = b.BucketStart
			if values[i] > peak {
				peak = values[i]
			}
		}
		unit = chartUnitCost
	case int(chartUnitRemaining): // remaining quota line chart
		pts5h, err5h := m.deps.Cache.UtilizationSince("five_hour_pct", from)
		pts7d, err7d := m.deps.Cache.UtilizationSince("seven_day_pct", from)
		if err5h != nil && err7d != nil {
			m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
			m.lastValues = nil
			m.lastStarts = nil
			m.lastPts5h = nil
			m.lastPts7d = nil
			m.peak = 0
			m.setX(0)
			return
		}
		if err5h != nil {
			pts5h = nil
		}
		if err7d != nil {
			pts7d = nil
		}
		m.lastPts5h = pts5h
		m.lastPts7d = pts7d
		peak = 1.0
		anchor := pts5h
		if len(anchor) == 0 {
			anchor = pts7d
		}
		values = make([]float64, len(anchor))
		starts = make([]time.Time, len(anchor))
		for i, p := range anchor {
			values[i] = max(0, 1.0-p.Pct/100.0)
			starts[i] = p.At
		}
		unit = chartUnitRemaining
	default: // tokens
		buckets, err := m.deps.Cache.TokenBuckets(zoom.Duration, from, to)
		if err != nil || len(buckets) == 0 {
			m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
			m.lastValues = nil
			m.lastStarts = nil
			m.peak = 0
			m.setX(0)
			return
		}
		values = make([]float64, len(buckets))
		starts = make([]time.Time, len(buckets))
		for i, b := range buckets {
			values[i] = float64(b.Tokens)
			starts[i] = b.BucketStart
			if values[i] > peak {
				peak = values[i]
			}
		}
		unit = chartUnitTokens
	}

	m.peak = peak
	m.lastValues = values
	m.lastStarts = starts

	chartH := m.chartHeight()
	var canvasW int
	if unit == chartUnitRemaining {
		// Mirror bar mode's canvas-width formula so 'z' zoom and 'u'
		// unit-toggle preserve the same time-range under the viewport's
		// left edge in both modes. Floor at chartWidth() so a short
		// usage_samples history still spans the visible area instead
		// of rendering in a narrow slice on the left.
		canvasW = zoom.CanvasWidth(bucketCountInRange(from, to, zoom.Duration))
		if canvasW < m.chartWidth() {
			canvasW = m.chartWidth()
		}
		m.viewport.SetContent(buildLineChart(m.lastPts5h, m.lastPts7d, from, to, canvasW, chartH, time.Now(), zoom, m.dateOrder))
	} else {
		canvasW = zoom.CanvasWidth(len(values))
		m.viewport.SetContent(buildChart(values, starts, peak, canvasW, chartH, time.Now(), zoom, unit, m.dateOrder))
	}
	m.lastChartFrom = from
	m.lastChartTo = to
	m.lastCanvasW = canvasW
	m.lastZoomStride = zoom.stride()

	// Restore the user's anchor. Three cases:
	//   - !hadAnchor (first load, or coming back from an empty-cache
	//     placeholder): pin to the new right edge.
	//   - wasPinned: user was at "now", keep them at "now" against the
	//     new canvas width.
	//   - else: map anchorTime → column in the new canvas.
	//
	// The viewport offset is set directly in column-space; the bucket-
	// indexed shadow (m.viewportXOffset) is derived as targetCol / stride.
	// At stride=1 (15m/1h), column-index == bucket-index. At stride=12
	// (24h with BarWidth=10/BarGap=2), the bucket-index is the bucket
	// that contains the leftmost visible column, matching the pre-refactor
	// behaviour where setX(bucketIdx) snapped to bucket-aligned offsets.
	stride := zoom.stride()
	rightEdgeCol := max(0, canvasW-m.viewport.Width)
	var targetCol int
	switch {
	case !hadAnchor, wasPinned:
		targetCol = rightEdgeCol
	default:
		targetCol = timeToColumn(anchorTime, canvasW, from, to)
		if targetCol > rightEdgeCol {
			targetCol = rightEdgeCol
		}
	}
	m.viewport.SetXOffset(targetCol)
	m.viewportXOffset = targetCol / stride
}

// emptyPlaceholder returns a w×h block with "no Claude sessions yet"
// centered in colorMuted — the empty-cache state of the chart viewport.
func emptyPlaceholder(w, h int) string {
	if h < 1 {
		h = 1
	}
	if w < 1 {
		w = 1
	}
	msg := lipgloss.NewStyle().Foreground(colorMuted).Render("no Claude sessions yet")
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

// chartWidth returns the available width for the viewport. Floors at 10
// so the ntcharts canvas never collapses to a degenerate width.
func (m Model) chartWidth() int {
	w := m.w - 2
	if w < 10 {
		return 10
	}
	return w
}

// visibleBuckets returns how many whole bars fit in the viewport at the
// active zoom's BarWidth+BarGap layout. Bucket-indexed throughout: 1
// unit = 1 bar. Derived from: n bars fit iff n*BarWidth + (n-1)*BarGap
// <= chartWidth(), so n <= (chartWidth + BarGap) / (BarWidth + BarGap).
// Floors at 1 so the chart never collapses to zero visible bars when
// BarWidth > chartWidth() (degenerate terminal width).
//
// BarWidth is clamped to ≥1 and BarGap to ≥0 so stride is always ≥1 —
// guards against a stride-zero divide panic if a future ZoomLevels
// literal sets BarGap = -BarWidth.
func (m Model) visibleBuckets() int {
	z := ZoomLevels[m.zoomIdx]
	stride := z.stride()
	gap := max(z.BarGap, 0)
	v := (m.chartWidth() + gap) / stride
	if v < 1 {
		return 1
	}
	return v
}

// chartHeight returns the available rows for the chart, leaving room for
// the bordered header box (4 rows: top border, bars row, burn-rate row,
// bottom border), two separators (2 rows), and the help footer (1 row).
// Total non-body overhead = 7 rows.
func (m Model) chartHeight() int {
	h := m.h - 7
	if h < 5 {
		return 5
	}
	return h
}

// progressWidth returns the rendered width of each of the two quota bars,
// which sit side-by-side inside the header box. Per-side fixed chrome
// (matched across both sides for symmetry — the prerequisite for centring
// the │ divider exactly):
//   - 3 cols dim label prefix ("5h " or "7d ")
//   - 1 col bar→time margin (barTimeGap)
//   - 6 cols right-aligned time slot ("4h 59m" worst case; 7d's "23:59"
//     fits in 5 cols and gets 1 col of leading pad to stay symmetric)
//
// Per-side fixed chrome total: 3 + 1 + 6 = 10 cols. The header box
// itself reserves 4 cols (border + padding), and a 3-col " │ " divider
// sits between the two halves. Total fixed chrome = 4 + 10 + 3 + 10 = 27,
// split across two bars.
//
// At odd parities of (W - 27), integer division gives a 1-col residual
// that lipgloss absorbs as a trailing pad inside the box. Doesn't affect
// divider centring because the divider is positioned relative to the
// symmetric chrome, not derived from total width.
func (m Model) progressWidth() int {
	w := (m.w - 27) / 2
	if w < 6 {
		return 6
	}
	return w
}

// newProgressBar builds a quota bar using the project's green → red
// gradient (Material 500 #4caf50 → #f44336). WithGradient — not
// WithScaledGradient — keeps each cell's colour fixed by its position
// on the bar's full width, so a 5%-filled bar shows only the leftmost
// (green) cells and red only surfaces as fill approaches 100%. That's
// the fuel-gauge reading: cool = headroom remaining, warm = approaching
// the limit. The actual fill amount is supplied at render time via
// progress.ViewAs.
func newProgressBar(w int) progress.Model {
	if w < 10 {
		w = 10
	}
	return progress.New(
		progress.WithWidth(w),
		progress.WithoutPercentage(),
		progress.WithGradient(QuotaGradientStart, QuotaGradientEnd),
	)
}
