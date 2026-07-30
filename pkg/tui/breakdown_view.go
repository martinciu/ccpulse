package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/termsafe"
)

const (
	// minCellW is the narrowest a single project cell renders legibly:
	// label + cost + tokens + pct with gaps.
	minCellW = 48
	// columnDivider separates packed columns; also used by tests to count
	// columns.
	columnDivider = " │ "
)

const (
	breakdownProjectsTitle = "Projects (visible window)"
	breakdownModelsTitle   = "Models (visible window)"
)

// breakdownRow is one row of a breakdown panel: a label plus the cost/tokens/
// share triple every panel reports. Both cache aggregate types map into it, so
// one renderer serves every kind and the boxes cannot drift apart (#475).
type breakdownRow struct {
	Label   string
	CostUSD float64
	Tokens  int64
	CostPct float64
}

// breakdownTitle is the box's title line for a kind. breakdownNone has no box,
// so it returns "".
func breakdownTitle(k breakdownKind) string {
	switch k {
	case breakdownProjects:
		return breakdownProjectsTitle
	case breakdownModels:
		return breakdownModelsTitle
	default:
		return ""
	}
}

// rowsFromProjects adapts project aggregates into renderer rows, preserving
// order (ProjectAggregates already sorts, with "(no project)" forced last).
func rowsFromProjects(aggs []cache.ProjectAggregate) []breakdownRow {
	if len(aggs) == 0 {
		return nil
	}
	rows := make([]breakdownRow, len(aggs))
	for i, a := range aggs {
		rows[i] = breakdownRow{Label: a.Label, CostUSD: a.CostUSD, Tokens: a.Tokens, CostPct: a.CostPct}
	}
	return rows
}

// rowsFromModels adapts model aggregates into renderer rows, preserving order
// (ModelAggregates already sorts, with "(unknown model)" forced last).
func rowsFromModels(aggs []cache.ModelAggregate) []breakdownRow {
	if len(aggs) == 0 {
		return nil
	}
	rows := make([]breakdownRow, len(aggs))
	for i, a := range aggs {
		rows[i] = breakdownRow{Label: a.Label, CostUSD: a.CostUSD, Tokens: a.Tokens, CostPct: a.CostPct}
	}
	return rows
}

// breakdownCellCols returns how many project cells pack side-by-side into an
// outer box width (border + 1 col padding each side subtracted). Shared by
// renderBreakdownBox (column layout) and breakdownHeight (rows needed) so the
// packing math cannot drift between the renderer and the height calc (#420).
func breakdownCellCols(outerWidth int) int {
	inner := max(outerWidth-4, 1)
	divW := lipgloss.Width(columnDivider)
	return max(1, (inner+divW)/(minCellW+divW))
}

// renderBreakdownBox renders rows as a bordered, multi-column table sized to
// width×height (outer dimensions, including border). Columns are packed to
// fit width (≥1); cells fill column-major (top spender top-left, read down
// then right). The synthetic trailing row (the "(no project)" or "(unknown
// model)" bucket) is expected last in rows — both aggregate queries guarantee
// this — and therefore lands in the final cell. Empty rows render a centered
// placeholder. When rows exceed the cell budget (cols × bodyRows), the final
// cell reads "…N more".
//
// Heights 1–3 occur only mid-slide (#416: the steady target is ≥ 4 or 0) and
// degrade gracefully — 1: top border, 2: closed border shell, 3: shell around
// the title row — always exactly `height` rows so View's per-frame height
// conservation holds at every animated height.
func renderBreakdownBox(title string, rows []breakdownRow, width, height int) string {
	if height <= 2 {
		return breakdownBoxShell(width, height)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorMuted).
		Width(max(width-2, 1)).
		Height(max(height-2, 1))

	inner := max(width-4, 1)   // minus border + 1 col padding each side
	innerH := max(height-2, 1) // rows inside the border (title + body)

	if len(rows) == 0 {
		return box.Render(lipgloss.Place(inner, innerH,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(colorMuted).Render("no activity in this window")))
	}

	// Height 3: a single inner row — the title, no body. The general layout
	// below always emits title + ≥1 body row (≥4 rows total), which would
	// overflow the box.
	if innerH < 2 {
		return box.Render(lipgloss.NewStyle().Foreground(colorMuted).Render(title))
	}

	// One row is spent on the title, so cells share the remaining innerH-1.
	bodyRows := max(innerH-1, 1)

	cols := breakdownCellCols(width)
	cellW := (inner - (cols-1)*lipgloss.Width(columnDivider)) / cols

	capacity := cols * bodyRows
	overflow := 0
	if len(rows) > capacity {
		overflow = len(rows) - (capacity - 1) // reserve last cell for "…N more"
		rows = rows[:capacity-1]
	}

	cells := make([]string, 0, len(rows)+1)
	for _, a := range rows {
		cells = append(cells, breakdownCell(a, cellW))
	}
	if overflow > 0 {
		cells = append(cells, lipgloss.NewStyle().Width(cellW).
			Foreground(colorMuted).Render(fmt.Sprintf("…%d more", overflow)))
	}

	// Balance cells column-major: each column holds rowsPerCol stacked
	// cells, filled top-to-bottom then left-to-right, so the top spender is
	// top-left and "(no project)"/overflow lands bottom-right. rowsPerCol ≤
	// bodyRows because len(cells) ≤ capacity = cols*bodyRows.
	rowsPerCol := (len(cells) + cols - 1) / cols
	columns := make([]string, 0, cols)
	for c := range cols {
		lo := c * rowsPerCol
		if lo >= len(cells) {
			break
		}
		hi := min(lo+rowsPerCol, len(cells))
		columns = append(columns, lipgloss.JoinVertical(lipgloss.Left, cells[lo:hi]...))
	}

	div := lipgloss.NewStyle().Foreground(colorMuted).Render(columnDivider)
	joined := make([]string, 0, len(columns)*2)
	for i, col := range columns {
		if i > 0 {
			joined = append(joined, div)
		}
		joined = append(joined, col)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, joined...)

	// Title via a styled top line inside the box.
	titleLine := lipgloss.NewStyle().Foreground(colorMuted).Render(title)
	return box.Render(lipgloss.JoinVertical(lipgloss.Left, titleLine, body))
}

// breakdownBoxShell renders the box's border rows alone at the degenerate
// heights the slide passes through (1: top border, 2: top+bottom — the fully
// squashed box), matching renderBreakdownBox's RoundedBorder + colorMuted so
// the shell reads as the same box. lipgloss cannot emit a bordered block
// with zero content rows, hence the manual border rows.
func breakdownBoxShell(width, height int) string {
	b := lipgloss.RoundedBorder()
	inner := max(width-2, 0)
	style := lipgloss.NewStyle().Foreground(colorMuted)
	top := style.Render(b.TopLeft + strings.Repeat(b.Top, inner) + b.TopRight)
	if height <= 1 {
		return top
	}
	bottom := style.Render(b.BottomLeft + strings.Repeat(b.Bottom, inner) + b.BottomRight)
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

// Slot widths for the fixed right-hand columns. Each value is right-aligned
// in its own slot so the columns line up vertically across stacked cells.
//
//	costSlotW  — wide enough for the widest realistic cost label from
//	             formatBarValue (e.g. "$1,234" = 6 cols, "$1.23M" = 7 cols).
//	             Using 7 to leave a comfortable margin.
//	tokenSlotW — widest compact value from formatTokenCount: "999k" / "9.9M"
//	             are 4 cols; using 4.
//	pctSlotW   — widest pct label is "100%" = 4 cols; using 4.
const (
	costSlotW  = 7
	tokenSlotW = 4
	pctSlotW   = 4
)

// breakdownCell renders one row into a fixed-width cell: label
// (left, truncated) + cost + tokens + pct (right-aligned, in that order).
// The cost/tokens/pct values each sit in a fixed-width right-aligned slot
// so they line up vertically across stacked cells.
func breakdownCell(r breakdownRow, w int) string {
	if w < 8 {
		w = 8
	}
	// Clamp the percentage here too, even though both aggregate queries
	// already clamp CostPct to [0, 100] (pkg/cache/models.go,
	// pkg/cache/projects.go): the renderer is the one place that sees
	// every row from every current and future aggregate kind, so pinning
	// the invariant here makes it structural rather than dependent on each
	// producer remembering to clamp. min/max do NOT bound NaN in Go —
	// min(100, NaN) is NaN — so an explicit IsNaN check is required; a NaN
	// CostPct is reachable via +Inf in cost_usd_estimate (#475.30).
	pct := r.CostPct
	switch {
	case math.IsNaN(pct) || pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}

	// MaxWidth(N).Inline(true) on every fixed-width slot (not just the
	// label) guarantees the one-row-per-cell budget structurally: Width()
	// alone is a minimum, not a cap, so an over-wide value (e.g.
	// formatBarValue's "$100,000" into costSlotW=7, or
	// formatTokenCount(math.MaxInt64) into tokenSlotW=4) would otherwise
	// wrap the cell across multiple rows (#475.31).
	slotStyle := lipgloss.NewStyle().Inline(true)
	cost := slotStyle.Width(costSlotW).MaxWidth(costSlotW).Align(lipgloss.Right).Render(
		formatBarValue(r.CostUSD, chartUnitCost))
	tokens := slotStyle.Width(tokenSlotW).MaxWidth(tokenSlotW).Align(lipgloss.Right).Render(
		formatTokenCount(r.Tokens))
	pctCell := slotStyle.Width(pctSlotW).MaxWidth(pctSlotW).Align(lipgloss.Right).Render(
		fmt.Sprintf("%d%%", int(math.Round(pct))))
	right := cost + "  " + tokens + "  " + pctCell
	rw := lipgloss.Width(right)
	labelW := max(w-rw-1, 3)
	// Inline(true) keeps the label on a single line: Width() alone would
	// word-wrap BEFORE MaxWidth() truncates, and nothing else caps line
	// count, so a label wider than labelW would render a multi-row cell
	// while callers budget exactly one row per cell (#475.2).
	label := lipgloss.NewStyle().Width(labelW).MaxWidth(labelW).Inline(true).
		Render(termsafe.Printable(r.Label))
	// Inline(true).MaxWidth(w) on the outer cell style makes the one-row
	// guarantee hold even if a future change to the slots/label above
	// regresses — the outer cap is a backstop, not a substitute for the
	// inner ones (#475.31).
	return lipgloss.NewStyle().Width(w).Inline(true).MaxWidth(w).Render(
		label + lipgloss.PlaceHorizontal(w-labelW, lipgloss.Right, right))
}
