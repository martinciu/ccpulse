package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func sampleAggs() []cache.ProjectAggregate {
	return []cache.ProjectAggregate{
		{RepoRoot: "/c/dotfiles", Label: "dotfiles", CostUSD: 4451, Tokens: 19_200_000, CostPct: 38},
		{RepoRoot: "/c/ccpulse", Label: "ccpulse", CostUSD: 3889, Tokens: 27_800_000, CostPct: 33},
		{RepoRoot: "/c/gruppo", Label: "gruppo", CostUSD: 1119, Tokens: 4_500_000, CostPct: 9},
		{RepoRoot: "", Label: "(no project)", CostUSD: 5, Tokens: 1000, CostPct: 0},
	}
}

func TestRenderProjectsBox_PacksColumnsByWidth(t *testing.T) {
	wide := renderProjectsBox(sampleAggs(), 200, 8)
	narrow := renderProjectsBox(sampleAggs(), 60, 8)
	if lipglossCols(wide) <= lipglossCols(narrow) {
		t.Errorf("wide terminal should pack >= columns than narrow")
	}
	// Every label must appear somewhere.
	for _, a := range sampleAggs() {
		if !strings.Contains(wide, a.Label) {
			t.Errorf("wide box missing %q", a.Label)
		}
	}
}

func TestRenderProjectsBox_EmptyPlaceholder(t *testing.T) {
	got := renderProjectsBox(nil, 80, 6)
	if !strings.Contains(got, "no activity in this window") {
		t.Errorf("empty aggs should render placeholder, got:\n%s", got)
	}
}

func TestRenderProjectsBox_NoProjectLast(t *testing.T) {
	// Single column (narrow) → reading order is top-to-bottom; "(no project)"
	// must be the last non-border line with content.
	got := renderProjectsBox(sampleAggs(), 50, 8)
	idxNoProj := strings.Index(got, "(no project)")
	idxCcpulse := strings.Index(got, "ccpulse")
	if idxNoProj < idxCcpulse {
		t.Errorf("(no project) should render after real projects in single-column")
	}
}

func TestRenderProjectsBox_Overflow(t *testing.T) {
	// 1 column (narrow) × bodyRows=2 → capacity 2; 10 aggs must overflow
	// into a "…N more" final cell rather than silently dropping rows.
	many := make([]cache.ProjectAggregate, 10)
	for i := range many {
		many[i] = cache.ProjectAggregate{Label: fmt.Sprintf("proj%d", i), CostUSD: float64(10 - i)}
	}
	got := renderProjectsBox(many, 50, 5)
	if !strings.Contains(got, "more") {
		t.Errorf("overflow should render a “…N more” cell, got:\n%s", got)
	}
}

// lipglossCols counts how many "column dividers" a rendered box contains on
// its busiest row, as a proxy for column count. Implementation detail of the
// test; compares relative width packing.
func lipglossCols(box string) int {
	maxPipes := 0
	for line := range strings.SplitSeq(box, "\n") {
		if n := strings.Count(line, columnDivider); n > maxPipes {
			maxPipes = n
		}
	}
	return maxPipes
}
