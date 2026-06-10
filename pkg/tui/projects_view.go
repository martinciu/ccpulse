package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

const (
	// minCellW is the narrowest a single project cell renders legibly:
	// label + cost + tokens + pct with gaps.
	minCellW = 48
	// columnDivider separates packed columns; also used by tests to count
	// columns.
	columnDivider = " │ "
	projectsTitle = "Projects (visible window)"
)

// renderProjectsBox renders aggs as a bordered, multi-column table sized to
// width×height (outer dimensions, including border). Columns are packed to
// fit width (≥1); cells fill column-major (top spender top-left, read down
// then right). The synthetic "(no project)" row is expected last in aggs
// (ProjectAggregates guarantees this) and therefore lands in the final cell.
// Empty aggs render a centered placeholder. When aggs exceed the cell budget
// (cols × bodyRows), the final cell reads "…N more".
//
// Heights 1–3 occur only mid-slide (#416: the steady target is ≥ 4 or 0) and
// degrade gracefully — 1: top border, 2: closed border shell, 3: shell around
// the title row — always exactly `height` rows so View's per-frame height
// conservation holds at every animated height.
func renderProjectsBox(aggs []cache.ProjectAggregate, width, height int) string {
	if height <= 2 {
		return projectsBoxShell(width, height)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorMuted).
		Width(max(width-2, 1)).
		Height(max(height-2, 1))

	inner := max(width-4, 1)   // minus border + 1 col padding each side
	innerH := max(height-2, 1) // rows inside the border (title + body)

	if len(aggs) == 0 {
		return box.Render(lipgloss.Place(inner, innerH,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(colorMuted).Render("no activity in this window")))
	}

	// Height 3: a single inner row — the title, no body. The general layout
	// below always emits title + ≥1 body row (≥4 rows total), which would
	// overflow the box.
	if innerH < 2 {
		return box.Render(lipgloss.NewStyle().Foreground(colorMuted).Render(projectsTitle))
	}

	// One row is spent on the title, so cells share the remaining innerH-1.
	bodyRows := max(innerH-1, 1)

	cols := max(1, (inner+lipgloss.Width(columnDivider))/(minCellW+lipgloss.Width(columnDivider)))
	cellW := (inner - (cols-1)*lipgloss.Width(columnDivider)) / cols

	capacity := cols * bodyRows
	overflow := 0
	if len(aggs) > capacity {
		overflow = len(aggs) - (capacity - 1) // reserve last cell for "…N more"
		aggs = aggs[:capacity-1]
	}

	cells := make([]string, 0, len(aggs)+1)
	for _, a := range aggs {
		cells = append(cells, projectCell(a, cellW))
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
	title := lipgloss.NewStyle().Foreground(colorMuted).Render(projectsTitle)
	return box.Render(lipgloss.JoinVertical(lipgloss.Left, title, body))
}

// projectsBoxShell renders the box's border rows alone at the degenerate
// heights the slide passes through (1: top border, 2: top+bottom — the fully
// squashed box), matching renderProjectsBox's RoundedBorder + colorMuted so
// the shell reads as the same box. lipgloss cannot emit a bordered block
// with zero content rows, hence the manual border rows.
func projectsBoxShell(width, height int) string {
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

// projectCell renders one project's row into a fixed-width cell: label
// (left, truncated) + cost + tokens + pct (right-aligned, in that order).
// The cost/tokens/pct values each sit in a fixed-width right-aligned slot
// so they line up vertically across stacked cells.
func projectCell(a cache.ProjectAggregate, w int) string {
	if w < 8 {
		w = 8
	}
	slotStyle := lipgloss.NewStyle()
	cost := slotStyle.Width(costSlotW).Align(lipgloss.Right).Render(
		formatBarValue(a.CostUSD, chartUnitCost))
	tokens := slotStyle.Width(tokenSlotW).Align(lipgloss.Right).Render(
		formatTokenCount(a.Tokens))
	pct := slotStyle.Width(pctSlotW).Align(lipgloss.Right).Render(
		fmt.Sprintf("%d%%", int(math.Round(a.CostPct))))
	right := cost + "  " + tokens + "  " + pct
	rw := lipgloss.Width(right)
	labelW := max(w-rw-1, 3)
	label := lipgloss.NewStyle().Width(labelW).MaxWidth(labelW).Render(a.Label)
	return lipgloss.NewStyle().Width(w).Render(
		label + lipgloss.PlaceHorizontal(w-labelW, lipgloss.Right, right))
}
