// Package tui implements the Bubble Tea model — header quota bars and the horizontally-scrollable token histogram.
package tui

import (
	"context"
	"fmt"
	"log/slog"
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

// horizontalScrollStep is the default per-keypress shift in BUCKETS for the
// finer zooms (15m/1h). 24h overrides it to 1 (one day per press) via
// ZoomLevel.ScrollStep. setX multiplies the bucket count by the per-zoom
// stride, so this is bucket-indexed — not columns.
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

// indexBannerClearMsg is the one-shot timer used when reduce_motion is
// enabled to dismiss the post-backfill "✓ indexed N" banner after its
// full-opacity dwell. The animations-on path uses the 3-step
// tickFadeMsg ladder instead.
type indexBannerClearMsg struct{}

// springTickMsg drives the per-bar harmonica spring loop after a
// 'u' unit-toggle. Scheduled by Update on the unit-key path and
// re-scheduled by the springTickMsg handler until all springs are
// settled, after which idle TUI cost returns to zero (no further
// Cmd is returned).
//
// gen is the animation generation the tick belongs to — captured at
// schedule time from Model.springGen. The handler drops ticks whose
// gen doesn't match the current generation so rapid 'u' presses can't
// stack independent tick loops (issue #218). Without the gate, each
// rapid press leaves the previous animation's already-rescheduled tick
// in flight; both ticks then advance the new animation, doubling
// (then tripling, etc.) the effective frame rate until the animation
// visibly skips.
type springTickMsg struct{ gen int }

// nowTickMsg fires when wall-clock time reaches the next bucket boundary,
// driving the live chart-window advance (#311). gen is matched against
// m.nowGen so a stale tick from a previous zoom's cadence is dropped
// (mirrors springTickMsg / springGen).
type nowTickMsg struct{ gen int }

// projectsTickMsg fires after scroll settles; the projects box recomputes
// only if gen still matches m.projectsGen (i.e. no later scroll superseded
// it). Mirrors the nowTickMsg generation guard (#311).
type projectsTickMsg struct{ gen int }

// projectsDebounce is the scroll-settle delay before the projects box
// re-queries. The chart itself scrolls live; only the box is debounced.
const projectsDebounce = 120 * time.Millisecond

// QuotaMsg is sent when fresh usage data is available.
type QuotaMsg struct {
	Usage     *anthro.Usage
	Source    string
	UpdatedAt time.Time
}

// Deps wires external dependencies into the TUI model.
type Deps struct {
	// Ctx is the program-lifetime ctx threaded into every Cache call
	// inside the TUI (CostBuckets, IOTokenBuckets, UtilizationSince,
	// EarliestMessageTime). A Model's lifetime IS the program lifetime,
	// so this stays on the Model rather than being passed through every
	// Update/View method. If nil, defaults to context.Background() —
	// useful for tests that construct Deps{} without touching the DB.
	Ctx context.Context

	Cache        *cache.Cache
	ProjectsRoot string
	Credential   anthro.Credential
	HasOAuth     bool
	CacheDir     string
	IsDev        bool
	// ReduceMotion disables the unit-toggle spring animation and the
	// index-banner fade ladder. Zero value = false = animations on,
	// preserving today's behaviour. Sourced from cfg.UI.ReduceMotion.
	ReduceMotion bool
}

// quotaSide identifies which quota bar a per-frame ratio belongs to.
// Used by quotaIntroRatio to dispatch to the right per-bar spring
// during the open-path slide-in (#192). Two-value enum, no parsing.
type quotaSide int

const (
	quotaSide5h quotaSide = iota
	quotaSide7d
)

// springKind tags which animation is currently in flight so handleSpringTick
// can dispatch the shared springTickMsg to the right machine. The unit toggle
// (two-phase, per-bar) and the zoom squeeze (single-phase, single-scalar) are
// mutually exclusive — refreshChart aborts any in-flight animation — so one
// master springActive flag plus this tag is sufficient. See issue #373.
type springKind int

const (
	springKindNone springKind = iota
	springKindUnit
	springKindZoom
	springKindProjects
)

// Model is the root Bubble Tea model for the chart view.
type Model struct {
	// ctx is the program-lifetime ctx threaded into every Cache call
	// inside the TUI. See Deps.Ctx for the convention rationale —
	// bubbletea Model lifetime IS program lifetime, so this storage is
	// the documented exception to "never store ctx in a struct".
	ctx context.Context

	deps       Deps
	keys       KeyMap
	progress   progress.Model // 5-hour quota bar
	progress7d progress.Model // 7-day quota bar

	progressScoped []progress.Model // per-model weekly limit bars, one per Window.ScopedLimits entry (#463)
	viewport       viewport.Model
	help           help.Model
	showHelp       bool

	zoomIdx int // index into ZoomLevels
	unitIdx int // 0 = cost, 1 = tokens, 2 = remaining. Cycled by 'u'. Resets to cost on launch.

	// now returns the current wall-clock time. Defaults to time.Now in New;
	// tests override it to drive deterministic bucket-boundary crossings
	// (#311). Every wall-clock read in the chart render/advance path goes
	// through this seam.
	now func() time.Time

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

	// oldValues / oldStarts are snapshotted in beginUnitAnimation so
	// renderSpringFrame can render the exiting bar chart (oldIsLine=false
	// phase) after refreshChart has already overwritten lastValues/Starts
	// with the new unit's data. Used by the bar branch during
	// springShrinking when oldIsLine=false — the bucket-aligned
	// counterpart to oldPts5h/7d.
	oldValues []float64
	oldStarts []time.Time

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

	// chartCache memoizes the bar-unit bucket arrays so refreshChart re-queries
	// only the mutable trailing region per refresh instead of re-aggregating
	// full history (#378). Distinct from lastChartFrom/lastValues, which serve
	// scroll-anchor preservation. See chart_cache.go.
	chartCache chartCache

	// underfilled is true when the indexed data is narrower than the chart
	// viewport, so refreshChart padded the [from, to) window leftward to span
	// the full width (#300). setX reads it to lock the viewport at the flush-
	// right offset (←/→ inert) while sparse; cleared once data fills the width.
	underfilled bool

	// hasData is true when refreshChart found real rows for the active unit
	// (messages for cost/tokens, usage samples for remaining). It is distinct
	// from len(lastValues) > 0: since #300 a truly empty cache produces a
	// zero-padded full-width axis (lastValues is non-empty), so the
	// animation/intro/recovery paths key off hasData — not bucket count — to
	// tell "warming up" (suppress animation, defer the intro) from real but
	// sparse data (animate and arm normally).
	hasData bool

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
	// springGen is bumped each time a new animation arms (beginUnitAnimation,
	// beginIntroAnimation, QuotaMsg late-arrival arm). Every tea.Tick →
	// springTickMsg{} schedule captures the current value, and the handler
	// drops ticks whose gen doesn't match. This is what stops rapid 'u'
	// presses from stacking the previous animation's still-pending tick on
	// top of the new animation's tick and accelerating it (#218).
	springGen int
	// springKind tags the in-flight animation (none/unit/zoom) so
	// handleSpringTick dispatches the shared springTickMsg correctly (#373).
	springKind springKind
	// Zoom-squeeze animation state (#373). Single critically-damped spring
	// driving r in [0,1]; the visible time window lerps oWin→nWin across the
	// squeeze. Distinct from the unit-toggle per-bar springs above.
	zoomSpring    harmonica.Spring
	zoomSpringR   float64
	zoomSpringVel float64
	zoomSnap      zoomAnimSnapshot
	// Projects-box slide (#416): single-phase spring on the box's OUTER
	// height. projectsAnimH is the animated height the projectsHeight()
	// lever returns mid-slide; projectsSlideFrom/To are the endpoints of
	// the in-flight slide (re-arm starts From at the current height).
	// Frames render through the STEADY pipelines (renderProjectsFrame), so
	// no snapshot state exists — endpoint frames equal the steady views by
	// construction. Mutually exclusive with the unit/zoom springs via the
	// shared springActive flag + springKind tag.
	projectsSpring    harmonica.Spring
	projectsSpringR   float64
	projectsSpringVel float64
	projectsAnimH     int
	projectsSlideFrom int
	projectsSlideTo   int
	// nowGen is bumped each time the live-advance tick is re-armed (zoom
	// change). scheduleNowTick captures the current value into the scheduled
	// nowTickMsg; the handler drops ticks whose gen doesn't match, so a zoom
	// switch can't leave a previous cadence's tick chain running (#311).
	nowGen int
	// projectsGen is bumped on every scroll; scheduleProjectsTick captures
	// it so a settled tick runs refreshProjects only when not superseded by
	// a later scroll (#311 generation-guard pattern).
	projectsGen int
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

	// introPending is true until the first non-empty refreshChart triggers
	// the open-path slide-in animation (or is cleared by reduce_motion).
	// One-shot: never re-armed after the first non-empty refresh. See #188.
	introPending bool

	// springIntro is true while the open-path intro animation is in flight
	// (springHolding → springGrowing seeded by beginIntroAnimation). Used
	// to suppress RefreshMsg's refreshChart so the initial-refresh race
	// from main.go's startup-time p.Send(RefreshMsg{}) doesn't hard-cut
	// the intro via refreshChart's spring-abort logic. Cleared on settle
	// in the springGrowing handler and in refreshChart's defensive abort
	// block. WindowSizeMsg still hard-cuts (terminal resize is an
	// explicit user action). See #188.
	springIntro bool

	// Per-bar scalar springs for the open-path quota-bar slide-in (#192).
	// Each side has its own harmonica.Spring + (ratio, velocity, target)
	// triple. Targets snapshot at arm time inside beginIntroAnimation,
	// then re-snapshot in the QuotaMsg handler if the async Anthropic
	// poller hadn't loaded m.quota yet at arm. Both gaps fold into the
	// existing springGrowing maxGap check so chart bucket springs +
	// 5h quota + 7d quota settle in the same frame.
	quotaSpring5h harmonica.Spring
	quotaRatio5h  float64
	quotaVel5h    float64
	quotaTarget5h float64
	quotaSpring7d harmonica.Spring
	quotaRatio7d  float64
	quotaVel7d    float64
	quotaTarget7d float64

	// quotaIntroPending tracks whether the open-path quota slide-in is
	// still owed to the user. The chart intro and quota intro share
	// introPending for arming, but quota animation requires m.quota
	// to be loaded — and the async poller often hasn't completed by
	// the time WindowSize triggers the chart arm. This flag stays true
	// past the chart arm if quota was nil, so when QuotaMsg eventually
	// arrives the handler can either re-snapshot in-flight targets
	// (during the intro) or kick a quota-only late-arrival animation
	// (after intro settle). Cleared on first quota animation, on
	// late-arrival fire, or under reduce_motion in New().
	quotaIntroPending bool

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

	// showProjects toggles the projects breakdown box (the `p` key). Default
	// true. When false, projectsHeight() returns 0 — the box is not rendered
	// and the chart reclaims its rows. Session-only; not persisted.
	showProjects bool

	// projectAggs is the last per-project rollup for the visible window,
	// rendered in the projects box below the chart. Recomputed by
	// refreshProjects on refresh/zoom and on the debounced scroll-settle.
	projectAggs []cache.ProjectAggregate

	w, h int

	// dateOrder is detected once at New() from LC_TIME / LC_ALL / LANG
	// and never mutated — locale doesn't change at runtime. Threaded
	// through buildChart → renderXLabels → formatXLabel → dateLabel
	// to drive the day-boundary stamp's MM/DD vs DD/MM order.
	dateOrder dateOrder
}

// New constructs a Model from the given Deps, initialising progress bars, viewport, and key bindings.
func New(d Deps) Model {
	if d.Ctx == nil {
		d.Ctx = context.Background()
	}
	m := Model{
		ctx:          d.Ctx,
		deps:         d,
		keys:         defaultKeyMap(),
		help:         help.New(),
		zoomIdx:      0, // default: 15m
		dateOrder:    detectDateOrder(),
		now:          time.Now,
		showProjects: false,
	}
	m.progress = newProgressBar(40)
	m.progress7d = newProgressBar(40)
	m.viewport = viewport.New(80, 20)
	m.viewport.SetHorizontalStep(horizontalScrollStep)
	m.introPending = !d.ReduceMotion
	m.quotaIntroPending = !d.ReduceMotion
	return m
}

// Init implements tea.Model; it fires the initial now-tick command.
func (m Model) Init() tea.Cmd { return m.scheduleNowTick() }

// Update implements tea.Model; it dispatches all Bubble Tea messages to the
// per-message handlers that drive the TUI state machine.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.handleWindowSize(msg)
	case IndexProgressMsg:
		return m, m.handleIndexProgress(msg)
	case tickFadeMsg:
		return m, m.handleTickFade()
	case indexBannerClearMsg:
		m.handleIndexBannerClear()
		return m, nil
	case springTickMsg:
		return m, m.handleSpringTick(msg)
	case QuotaMsg:
		return m, m.handleQuotaMsg(msg)
	case RefreshMsg:
		return m, m.handleRefresh()
	case nowTickMsg:
		return m, m.handleNowTick(msg)
	case projectsTickMsg:
		m.handleProjectsTick(msg)
		return m, nil
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

// handleWindowSize re-lays out the viewport, progress bars, and help width on
// terminal resize, then re-queries the chart and (re)arms the intro.
//
// viewport.Width is set before refreshChart because renderWindow reads
// m.viewport.Width to build the content. viewport.Height is set AFTER
// refreshChart: refreshChart aborts any in-flight projects slide
// (springActive=false, springKind=None), which changes what chartHeight()
// returns. Assigning Height before the abort would bake the mid-slide value
// into the viewport, leaving it desynced until the next resize or 'p' press.
func (m *Model) handleWindowSize(msg tea.WindowSizeMsg) tea.Cmd {
	m.w, m.h = msg.Width, msg.Height
	m.viewport.Width = m.chartWidth()
	// help.Width controls when ShortHelp ellipsizes; if left at 0
	// the footer can wrap onto the body row and break chartHeight().
	m.help.Width = m.w
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())
	m.rebuildScopedBars()
	m.refreshChart()
	// Assign after refreshChart so the abort of any in-flight spring is
	// reflected in the height (chartHeight() reads projectsHeight(), which
	// reads projectsAnimH when springKind==springKindProjects).
	m.viewport.Height = m.chartHeight()
	return m.maybeArmIntro()
}

// handleIndexProgress tracks backfill progress and starts the post-backfill
// banner (fade ladder, or a single dwell under reduce-motion) on the
// active→idle falling edge.
func (m *Model) handleIndexProgress(msg IndexProgressMsg) tea.Cmd {
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
		// Falling edge — start the post-backfill banner.
		m.indexFadeStop = 1
		if m.deps.ReduceMotion {
			// Reduce-motion: one full-opacity dwell, no fade ladder.
			return tea.Tick(indexBannerDwellDuration, func(time.Time) tea.Msg {
				return indexBannerClearMsg{}
			})
		}
		return tea.Tick(indexFadeStepDuration, func(time.Time) tea.Msg {
			return tickFadeMsg{}
		})
	}
	return nil
}

// handleTickFade advances the post-backfill checkmark fade ladder, stopping
// after indexFadeStopCount steps.
func (m *Model) handleTickFade() tea.Cmd {
	if m.indexFadeStop == 0 {
		// Stale tick — no fade in progress. Drop silently.
		return nil
	}
	m.indexFadeStop++
	if m.indexFadeStop > indexFadeStopCount {
		m.indexFadeStop = 0
		return nil
	}
	return tea.Tick(indexFadeStepDuration, func(time.Time) tea.Msg {
		return tickFadeMsg{}
	})
}

// handleIndexBannerClear dismisses the reduce-motion post-backfill banner.
func (m *Model) handleIndexBannerClear() {
	if m.indexFadeStop == 0 {
		// Stale tick — banner already dismissed (e.g. user re-entered
		// indexing mid-dwell). Drop silently.
		return
	}
	m.indexFadeStop = 0
}

// handleRefresh recomputes the window and re-queries the chart on a watcher
// event, suppressing the chart rebuild while the open-path intro is running.
func (m *Model) handleRefresh() tea.Cmd {
	start := time.Now()
	m.recomputeWindow()
	// Suppress refreshChart while the intro is in flight so the
	// startup-time RefreshMsg race (cmd/ccpulse/main.go:329 +
	// watcher events) doesn't hard-cut the intro via refreshChart's
	// spring-abort block. The intro's terminal springGrowing tick
	// fires its own refreshChart after settle (~600 ms), so any
	// data updates that arrived during the intro are picked up
	// there.
	if !m.springIntro {
		m.refreshChart()
	}
	slog.Debug("tui.refreshMsg",
		"dur_ms", time.Since(start).Milliseconds(),
		"zoom", ZoomLevels[m.zoomIdx].Label)
	return m.maybeArmIntro()
}

// handleNowTick advances the live chart window at a bucket boundary and
// re-arms the next tick, dropping stale cadence chains (#311).
func (m *Model) handleNowTick(msg nowTickMsg) tea.Cmd {
	if msg.gen != m.nowGen {
		// Stale chain from a previous zoom cadence — drop without
		// rescheduling so duplicate chains can't accumulate (#311).
		return nil
	}
	// Don't hard-cut an animation that owns the viewport: the startup intro
	// (springIntro) and the zoom squeeze (springKindZoom) both freeze the right
	// edge and repaint it themselves. We still reschedule below, so the chain
	// stays alive and the advance resumes the instant the animation settles —
	// the squeeze deliberately leaves the chain un-bumped so an abort can't
	// orphan it (#373). refreshChart's wasPinned/anchorTime block does the rest
	// — pinned advances to the new right edge, scrolled stays anchored.
	animatingViewport := m.springIntro || (m.springActive && m.springKind == springKindZoom)
	if !animatingViewport {
		m.refreshChart()
	}
	// One log line per boundary crossing — the live-advance cadence is
	// one wakeup per bucket, so this is not a per-frame hot path. Logs
	// only the zoom label (no paths, tokens, or credentials).
	slog.Debug("tui.nowTick", "zoom", ZoomLevels[m.zoomIdx].Label)
	return m.scheduleNowTick()
}

// scheduleProjectsTick bumps the generation and arms a settle tick. The
// pointer receiver makes the bump persist after Update returns, so the
// captured gen identifies this scroll burst; a later scroll bumps the gen
// again and supersedes this tick. The chart scrolls live — only the
// projects box recompute is debounced.
func (m *Model) scheduleProjectsTick() tea.Cmd {
	if !m.showProjects {
		return nil
	}
	m.projectsGen++
	gen := m.projectsGen
	return tea.Tick(projectsDebounce, func(time.Time) tea.Msg {
		return projectsTickMsg{gen: gen}
	})
}

// handleProjectsTick recomputes the projects box if this tick is the latest
// scheduled (gen matches); otherwise it was superseded by a later scroll and
// is dropped. The settle is also where the content-aware box height reflows
// (#420): refreshProjects may change the row count, so re-sync the layout.
// No Cmd to return — the settle chain ends here.
func (m *Model) handleProjectsTick(msg projectsTickMsg) {
	if msg.gen != m.projectsGen {
		return
	}
	// Do not reflow mid-spring: a unit or zoom spring in flight owns m.peak as
	// the bar-height normalization base, and applyProjectsResize calls
	// renderWindow (bar mode) / buildLineChart (remaining mode) which would
	// overwrite it, corrupting the spring frames and flashing steady-state
	// content (#420). The deferred recompute is never lost — every spring
	// settle path calls refreshChart (pkg/tui/springs.go, pkg/tui/zoomspring.go,
	// and the projects slide in pkg/tui/projectsspring.go, #416), whose
	// pre-paint refreshProjects + height re-sync catches it.
	if m.springActive {
		return
	}
	m.refreshProjects()
	m.applyProjectsResize()
}

// handleQuotaMsg records fresh usage, recomputes the window, and resolves the
// open-path quota intro: re-snapshot targets while the intro is in flight, or
// kick a late-arrival quota-only slide-in if it already settled (#192).
func (m *Model) handleQuotaMsg(msg QuotaMsg) tea.Cmd {
	m.quota = msg.Usage
	m.quotaSource = msg.Source
	m.quotaUpdatedAt = msg.UpdatedAt
	m.recomputeWindow()
	// (#463) recomputeWindow can change scopedRowCount() (e.g. 0→N on
	// first quota arrival, or N changing on a later poll), which grows
	// or shrinks headerContentRows() and therefore chartHeight(). Scoped
	// rows render immediately — they don't participate in the intro
	// spring — so without this the viewport stays sized for the old
	// header and View() overflows m.h until the next watcher event or
	// chart-affecting keypress. Skipped while a spring is active:
	// mid-spring heights are owned by the animation, and every settle
	// path (springs.go, zoomspring.go, projectsspring.go) already calls
	// refreshChart, which re-syncs.
	if !m.springActive {
		m.applyProjectsResize()
	}
	// (#192) Quota arrival timing fix. Two paths:
	//
	// 1. In-flight: the open-path intro is still running but the
	//    quota targets were snapshotted as 0 because m.quota was
	//    nil at arm. Re-snapshot to live values so the springs
	//    ease toward real targets for the remainder of the grow.
	// 2. Late arrival: the chart intro armed and settled with no
	//    quota loaded (springs ran 0→0 invisibly). Kick a
	//    quota-only slide-in now, skipping the hold beat (the
	//    bars already sat at 0 throughout the chart intro — no
	//    new beat to register).
	if m.springIntro {
		m.quotaTarget5h = float64(m.window.Percent) / 100.0
		if m.window.Has7d {
			m.quotaTarget7d = float64(m.window.Percent7d) / 100.0
		} else {
			m.quotaTarget7d = 0
		}
		m.quotaIntroPending = false
		return nil
	}
	if m.quotaIntroPending && !m.introPending && !m.deps.ReduceMotion {
		return m.kickLateArrivalQuotaIntro()
	}
	return nil
}

// handleKey dispatches a key press: quit, help toggle, zoom, unit toggle, and
// horizontal scroll. Chart-affecting keys are suppressed while help is shown.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case msg.String() == "ctrl+c":
		return tea.Quit
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
	case m.showHelp:
		// Suppress chart-affecting keys while the help overlay is up,
		// so dismissing help returns the user to the same scroll/zoom
		// state they left.
	case key.Matches(msg, m.keys.Zoom):
		return m.handleZoomKey()
	case key.Matches(msg, m.keys.Unit):
		return m.handleUnitKey()
	case key.Matches(msg, m.keys.Projects):
		return m.handleProjectsKey()
	case key.Matches(msg, m.keys.ScrollLeft):
		m.scrollLeft(ZoomLevels[m.zoomIdx].ScrollStep)
		return m.scheduleProjectsTick()
	case key.Matches(msg, m.keys.ScrollRight):
		m.scrollRight(ZoomLevels[m.zoomIdx].ScrollStep)
		return m.scheduleProjectsTick()
	}
	return nil
}

// handleUnitKey cycles the chart unit and arms the two-phase toggle animation
// (or snaps directly under reduce-motion).
func (m *Model) handleUnitKey() tea.Cmd {
	m.unitIdx = (m.unitIdx + 1) % int(chartUnitCount)
	if m.deps.ReduceMotion {
		// Snap directly: no spring state, no tick scheduling.
		// refreshChart is the same call that beginUnitAnimation
		// makes internally — without it the viewport keeps showing
		// the old unit's content.
		m.refreshChart()
		return nil
	}
	m.beginUnitAnimation()
	if !m.springActive {
		return nil
	}
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
	gen := m.springGen
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// handleProjectsKey slides the projects box up (show) / down (hide) via a
// harmonica spring (#416). reduce_motion, a too-short terminal (no room for
// a box), or an empty/cleared chart (renderWindow would no-op against no
// content) → snap, the pre-#416 hard cut.
func (m *Model) handleProjectsKey() tea.Cmd {
	if m.deps.ReduceMotion || m.projectsTargetHeight() == 0 || m.lastCanvasW == 0 {
		m.showProjects = !m.showProjects
		m.viewport.Height = m.chartHeight()
		m.refreshChart()
		return nil
	}
	m.beginProjectsAnimation()
	if !m.springActive {
		return nil
	}
	gen := m.springGen
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// View implements tea.Model; it renders the full TUI frame — header quota bars, separator, and token histogram.
func (m Model) View() string {
	if m.w == 0 {
		return "" // pre-init; don't time
	}
	if m.w < MinWidth || m.h < MinHeight {
		return renderTooSmall(m.w, m.h)
	}
	start := time.Now()
	header := renderHeader(m.w, m.quotaBars())
	sep := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", m.w))
	var body string
	if m.showHelp {
		body = m.help.FullHelpView(m.keys.FullHelp())
	} else {
		body = m.renderChartBody(m.viewport.View())
	}
	footer := m.renderFooter()

	parts := []string{header, sep, body}
	// The projects box sits between chart and footer, suppressed while the
	// help overlay is up (help replaces the chart body, so the box would be
	// out of place). projectsHeight() returns the animated height mid-slide,
	// so the SAME render path produces steady and slide frames — the box
	// re-flows at each height: real borders and title from the first frames,
	// cells filling top-down, the "…N more" overflow recounting as rows fit
	// (#416 round two; round one's pre-rendered bottom-slice revealed blank
	// padding first).
	if !m.showHelp {
		if ph := m.projectsHeight(); ph > 0 {
			parts = append(parts, renderProjectsBox(m.projectAggs, m.w, ph))
		}
	}
	parts = append(parts, sep, footer)
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
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

// renderChartBody overlays the Y-axis labels/ticks onto the viewport's chart
// body, picking the variant for the current animation/unit state:
//   - zoom squeeze: full-fade y-ticks — only the visible window moves (#373)
//   - unit-toggle / intro spring: fade the OLD/NEW axis by the spring envelope
//   - steady remaining (line) mode: full y-ticks
//   - steady bar mode: full y-label at the active unit's peak
//
// Split out of View() so each stays under the cyclomatic-complexity gate; the
// switch is evaluated top-down, so the zoom case must precede the generic
// springActive case.
func (m Model) renderChartBody(rawBody string) string {
	switch {
	case m.springActive && m.springKind == springKindZoom:
		// Line mode: the line is fully present throughout (only the visible
		// window moves), so y-ticks render at full fade. Bar mode: cross-fade
		// the peak y-label across the morph — outgoing oPeak over r<0.5, blank
		// at the midpoint, incoming nPeak over r>0.5, each side gated on its
		// own zoom's in-bar-numbers (24h suppresses the label) (#393).
		if m.zoomSnap.unit == chartUnitRemaining {
			return overlayYTicks(rawBody, m.chartHeight(), 1.0)
		}
		return barZoomYLabel(rawBody, m.zoomSnap, ZoomLevels[m.zoomIdx], m.chartHeight(), m.zoomSpringR)
	case m.springActive && m.springKind == springKindProjects:
		// Height-only animation: the y-overlay is EXACTLY the steady-state
		// overlay at the frame's (animated) chartHeight — same live inputs
		// as the steady cases below, so endpoint frames match them
		// byte-for-byte (#416 round two). m.peak is recomputed by
		// renderWindow each frame from the same fixed window (constant
		// during the slide). MUST precede the generic springActive case,
		// which reads m.springRatios (unit-toggle state, unset here).
		if chartUnit(m.unitIdx) == chartUnitRemaining {
			return overlayYTicks(rawBody, m.chartHeight(), 1.0)
		}
		return overlayYLabel(rawBody, m.peak, chartUnit(m.unitIdx),
			m.chartHeight(), 1.0, ZoomLevels[m.zoomIdx].hasInBarNumbers())
	case m.springActive:
		var maxR float64
		for _, r := range m.springRatios {
			maxR = max(maxR, r)
		}
		fade := maxR
		labelUnit := chartUnit(m.unitIdx)
		labelPeak := m.peak
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
			return overlayYTicks(rawBody, m.chartHeight(), fade)
		}
		return overlayYLabel(rawBody, labelPeak, labelUnit, m.chartHeight(), fade, ZoomLevels[m.zoomIdx].hasInBarNumbers())
	case chartUnit(m.unitIdx) == chartUnitRemaining:
		return overlayYTicks(rawBody, m.chartHeight(), 1.0)
	default:
		return overlayYLabel(rawBody, m.peak, chartUnit(m.unitIdx), m.chartHeight(), 1.0, ZoomLevels[m.zoomIdx].hasInBarNumbers())
	}
}

// barZoomYLabel cross-fades the bar-chart peak y-label across one 'z' zoom
// morph: the outgoing peak (oPeak at the OLD zoom's in-bar-numbers gate) fades
// out over r<0.5, blank at the midpoint, the incoming peak (nPeak at the NEW
// zoom) fades in over r>0.5. The line-mode counterpart is overlayYTicks at full
// fade; this is the bar-mode treatment, mirroring renderZoomFrame's x-label
// cross-fade for the y-axis (#393). overlayYLabel is a no-op at fade<=0 (so the
// midpoint frame is blank) and when the gated zoom shows in-bar numbers (24h),
// so a side morphing to/from 24h suppresses its y-label exactly as steady state
// does. Extracted from renderChartBody to keep that switch under the
// cyclomatic-complexity gate.
func barZoomYLabel(rawBody string, snap zoomAnimSnapshot, newZoom ZoomLevel, chartH int, r float64) string {
	switch {
	case r < 0.5:
		fade := (0.5 - r) / 0.5
		return overlayYLabel(rawBody, snap.oPeak, snap.unit, chartH, fade, snap.oZoom.hasInBarNumbers())
	case r > 0.5:
		fade := (r - 0.5) / 0.5
		return overlayYLabel(rawBody, snap.nPeak, snap.unit, chartH, fade, newZoom.hasInBarNumbers())
	default:
		return rawBody // r == 0.5: blank midpoint (overlayYLabel fade<=0 is a no-op anyway)
	}
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
	resetTxt := "idle"
	if m.window.MinutesToReset != nil {
		resetTxt = durString(*m.window.MinutesToReset)
	}
	left := renderQuotaSide(
		"5h ",
		m.progress,
		m.quotaIntroRatio(quotaSide5h, float64(m.window.Percent)/100.0),
		resetTxt,
	)
	// Derive the per-side slot from the actual rendered bars-row left,
	// not from a theoretical formula: newProgressBar clamps to a 10-col
	// minimum even when progressWidth() returns less, so the theoretical
	// per-side width drifts from the rendered width at narrow terminals
	// (the "clamp regime"). Reading lipgloss.Width(left) guarantees the
	// burn-rate row's slots line up with the bars row regardless of clamp.
	slotW := lipgloss.Width(left)

	var right string
	if m.window.Has7d && m.window.MinutesToReset7d != nil {
		right = renderQuotaSide(
			"7d ",
			m.progress7d,
			m.quotaIntroRatio(quotaSide7d, float64(m.window.Percent7d)/100.0),
			formatReset7d(*m.window.MinutesToReset7d),
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

	rows := []string{barsRow, burnRow}
	// Scoped per-model weekly rows (#463): one full-width row per
	// weekly_scoped limits entry, presence-gated — most accounts have
	// none and render exactly the two rows above. Count is capped by the
	// built bars (scopedRowCount) so a transient window/bars mismatch
	// degrades to fewer rows, never a panic.
	for i := range m.scopedRowCount() {
		sl := m.window.ScopedLimits[i]
		reset := ""
		if sl.MinutesToReset != nil {
			reset = durString(*sl.MinutesToReset)
		}
		rows = append(rows, renderScopedLimitRow(
			scopedLabel(sl.Model), m.progressScoped[i], float64(sl.Percent)/100.0, reset))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// nextBoundary returns the wall-clock instant of the END of the bucket
// containing now for the given zoom — i.e. the right-edge "to" the chart
// pins to. Single source of truth for both refreshChart's window edge and
// the live-advance tick's schedule (#311): the chart's right edge IS the
// next bucket boundary, so a tick scheduled to fire at nextBoundary(now)
// lands exactly when a new empty bucket should appear.
//
// 24h uses local-midnight boundaries (DST-correct via AddDate); sub-day
// zooms use UTC-aligned BucketAlign. Always returns an instant strictly
// after now (the current bucket's end), so a derived tick duration is
// never zero or negative.
func nextBoundary(now time.Time, zoom ZoomLevel) time.Time {
	if zoom.Duration == 24*time.Hour {
		return cache.DayStartLocal(now).AddDate(0, 0, 1)
	}
	return cache.BucketAlign(now, zoom.Duration).Add(zoom.Duration)
}

// scheduleNowTick returns a command that fires nowTickMsg at the next bucket
// boundary for the active zoom (#311). Self-rescheduled by the nowTickMsg
// handler and re-armed (with a bumped nowGen) on zoom change, so the cadence
// follows the current zoom and stale chains are dropped. nextBoundary is
// always strictly after now, so the duration is always positive.
func (m Model) scheduleNowTick() tea.Cmd {
	gen := m.nowGen
	now := m.now()
	d := nextBoundary(now, ZoomLevels[m.zoomIdx]).Sub(now)
	return tea.Tick(d, func(time.Time) tea.Msg {
		return nowTickMsg{gen: gen}
	})
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
	if w, err := status.Compute(m.ctx, m.deps.Cache.DB(), time.Now(), in); err == nil {
		m.window = w
	}
	m.progress = newProgressBar(m.progressWidth())
	m.progress7d = newProgressBar(m.progressWidth())
	m.rebuildScopedBars()
}

// scopedRowCount is the number of scoped-limit rows quotaBars will render:
// the window's entries capped by the built bars, so a transient mismatch
// (window updated before rebuildScopedBars runs) can never index past
// progressScoped. headerContentRows derives from this same count so the
// height math always matches what is painted.
func (m Model) scopedRowCount() int {
	return min(len(m.window.ScopedLimits), len(m.progressScoped))
}

// headerContentRows is the number of content rows inside the bordered
// header box: bars row + burn-rate row + one per rendered scoped-limit
// row. chartHeight and projectsTargetHeight derive their overhead from
// this so the frame never overflows when scoped rows are present.
func (m Model) headerContentRows() int {
	return 2 + m.scopedRowCount()
}

// scopedBarWidth sizes a scoped-limit row's bar so the row's right edge
// aligns with the bars row above it: the bars row's total width (two
// symmetric sides plus the " │ " divider) minus this row's own chrome
// (label + 1-col gap + reset slot). Same minBarWidth floor as the 5h/7d
// bars so narrow terminals degrade instead of wrapping.
func (m Model) scopedBarWidth(labelW int) int {
	const perSideChrome = 3 + 1 + statusBlockMaxW // "5h " label + gap + time slot
	barsRowW := 2*(perSideChrome+m.progressWidth()) + 3
	w := barsRowW - labelW - 1 - statusBlockMaxW
	if w < minBarWidth {
		return minBarWidth
	}
	return w
}

// rebuildScopedBars re-derives the per-entry progress models from the
// current window and terminal width. Called wherever progress/progress7d
// are rebuilt (resize, recomputeWindow) — entry count and label widths can
// change on every quota poll. Reassigns to nil for copy-safety: Model is
// copied by value in bubbletea's Update/View loop, so reslicing the backing
// array would mutate prior copies. nil reassignment keeps value-copies independent.
func (m *Model) rebuildScopedBars() {
	m.progressScoped = nil
	for _, sl := range m.window.ScopedLimits {
		labelW := lipgloss.Width(scopedLabel(sl.Model))
		m.progressScoped = append(m.progressScoped, newProgressBar(m.scopedBarWidth(labelW)))
	}
}

// refreshProjects recomputes the per-project rollup for the chart's
// currently-visible window and stores it in m.projectAggs. Cheap: the
// window is bounded by what's on screen. Safe when the cache is nil or the
// chart has no data (clears to empty → placeholder — including remaining
// mode with zero usage samples, where the warming-up chart and an empty
// box tell the same no-data story).
//
// The [from, to) window is mode-aware (#430):
//
//   - Bar modes (tokens/cost): derived from the same lastStarts/
//     viewportXOffset/visibleBuckets the bar chart renders — exact bucket
//     edges, so the box reconciles with the visible bars.
//
//   - Remaining mode: taken from visibleWindow(), the single source of
//     truth for the on-screen time range. Here lastStarts holds sparse
//     usage_samples timestamps (one per usage-API fetch) while
//     viewportXOffset stays a canvas bucket index, so the bar-mode
//     indexing would clamp onto the latest sample and query a window of
//     minutes — empty unless a message landed after the newest sample
//     (the "no activity in this window" symptom). setX already
//     special-cases the same sparse-lastStarts mismatch for its clamp.
func (m *Model) refreshProjects() {
	if !m.showProjects {
		m.projectAggs = nil
		return
	}
	if m.deps.Cache == nil || len(m.lastStarts) == 0 {
		m.projectAggs = nil
		return
	}
	var from, to time.Time
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		from, to = m.visibleWindow()
	} else {
		start := max(0, m.viewportXOffset)
		if start >= len(m.lastStarts) {
			start = len(m.lastStarts) - 1
		}
		end := min(start+m.visibleBuckets(), len(m.lastStarts))
		from = m.lastStarts[start]
		to = m.lastChartTo
		if end < len(m.lastStarts) {
			to = m.lastStarts[end]
		}
	}
	aggs, err := m.deps.Cache.ProjectAggregates(m.ctx, from, to)
	if err != nil {
		slog.Debug("tui.refreshProjects", "err", err)
		m.projectAggs = nil
		return
	}
	m.projectAggs = aggs
}

// applyProjectsResize re-syncs the viewport height and chart content after a
// projectAggs change moved the content-aware projectsHeight (#420). No-op
// when the height is already in sync — the common case; most scroll-settles
// don't cross a row-count boundary. Never re-queries the cache: bar mode
// re-renders the in-memory visible window; remaining mode rebuilds the line
// chart from lastPts5h/7d. Height is a fixed point after one call (resizing
// never changes projectAggs), so callers never loop.
func (m *Model) applyProjectsResize() {
	nh := m.chartHeight()
	if m.viewport.Height == nh {
		return
	}
	m.viewport.Height = nh
	if m.lastCanvasW == 0 {
		return // cleared/pre-init chart: no content to re-render
	}
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		m.viewport.SetContent(buildLineChart(m.lastPts5h, m.lastPts7d,
			m.lastChartFrom, m.lastChartTo, m.lastCanvasW, nh, m.now(),
			ZoomLevels[m.zoomIdx], m.dateOrder, "projects-resize", ""))
		m.setX(m.viewportXOffset)
	} else {
		m.renderWindow()
	}
}

// minBarWidth is the smallest a quota bar may shrink to. Lowered from the
// former 6/10 floors so the two bars shrink to fit narrow terminals and
// the bars row stops overflowing the box (#320): the row fits whenever
// bar ≤ (m.w - chrome)/2, which progressWidth returns exactly — the old
// floors forced a too-wide bar below ~48 cols and lipgloss wrapped the
// row. Below ~31 cols the label/reset/divider chrome alone exceeds the
// width and nothing helps; a 1-col bar is degenerate but keeps the box
// intact down to that point.
const minBarWidth = 1

// newProgressBar builds a quota bar using the project's green → red
// gradient (Material 500 #4caf50 → #f44336). WithGradient — not
// WithScaledGradient — keeps each cell's colour fixed by its position
// on the bar's full width, so a 5%-filled bar shows only the leftmost
// (green) cells and red only surfaces as fill approaches 100%. That's
// the fuel-gauge reading: cool = headroom remaining, warm = approaching
// the limit. The actual fill amount is supplied at render time via
// progress.ViewAs.
func newProgressBar(w int) progress.Model {
	if w < minBarWidth {
		w = minBarWidth
	}
	return progress.New(
		progress.WithWidth(w),
		progress.WithoutPercentage(),
		progress.WithGradient(QuotaGradientStart, QuotaGradientEnd),
	)
}
