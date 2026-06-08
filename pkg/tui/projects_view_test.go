package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// stripANSI removes ANSI escape sequences from s so position arithmetic on
// the result is purely over visible characters.
func stripANSI(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// consume until 'm' (or any final byte 0x40–0x7e)
			i += 2
			for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
				i++
			}
			if i < len(s) {
				i++ // consume the final byte
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

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

// TestProjectCell_TokenCompactSuffix checks that token values above 999 are
// rendered with a k/M suffix and no comma (e.g. "65k", not "65,000").
func TestProjectCell_TokenCompactSuffix(t *testing.T) {
	cases := []struct {
		tokens  int64
		wantSub string
		wantNot string
		desc    string
	}{
		{65_000, "65k", "65,000", "thousands"},
		{472_266, "472k", "472,266", "hundreds of thousands"},
		{4_300_000, "4M", "4,300,000", "millions"},
		{742, "742", "", "sub-1000 raw"},
	}
	const cellW = 60
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			a := cache.ProjectAggregate{
				Label:   "testproj",
				CostUSD: 1.23,
				Tokens:  tc.tokens,
				CostPct: 5,
			}
			rendered := stripANSI(projectCell(a, cellW))
			if !strings.Contains(rendered, tc.wantSub) {
				t.Errorf("projectCell(%d tokens): want %q in rendered output\ngot: %q",
					tc.tokens, tc.wantSub, rendered)
			}
			if tc.wantNot != "" && strings.Contains(rendered, tc.wantNot) {
				t.Errorf("projectCell(%d tokens): must NOT contain %q in rendered output\ngot: %q",
					tc.tokens, tc.wantNot, rendered)
			}
		})
	}
}

// TestProjectCell_TokenColumnAlignment checks that the token sub-field's right
// edge is at a constant visual column across cells whose token values have
// different widths (e.g. "65k" vs "4M" vs "742").
//
// The right block is: [costSlotW][2sp][tokenSlotW][2sp][pctSlotW]
// Token right edge is always at offset costSlotW+2+tokenSlotW from the start
// of the right block, i.e. at character position (cellW - 2 - pctSlotW - 1)
// from the end (0-indexed from right). We assert it by verifying the two
// characters immediately after the token slot are always "  " (the inter-slot
// gap), for all cells rendered at the same cellW.
func TestProjectCell_TokenColumnAlignment(t *testing.T) {
	const cellW = 60
	// rightWidth = costSlotW + "  " + tokenSlotW + "  " + pctSlotW
	rightWidth := costSlotW + 2 + tokenSlotW + 2 + pctSlotW

	aggs := []cache.ProjectAggregate{
		{Label: "alpha", CostUSD: 10, Tokens: 65_000, CostPct: 40},  // "65k"  (3 chars → padded to 4)
		{Label: "beta", CostUSD: 5, Tokens: 4_300_000, CostPct: 20}, // "4M"   (2 chars → padded to 4)
		{Label: "gamma", CostUSD: 2, Tokens: 742, CostPct: 10},      // "742"  (3 chars → padded to 4)
		{Label: "delta", CostUSD: 1, Tokens: 472_266, CostPct: 5},   // "472k" (4 chars → fills slot)
	}

	// The inter-slot gap between tokens and pct is at position:
	//   (cellW - rightWidth) + costSlotW + 2 + tokenSlotW
	// from the left (0-indexed) in the plain rendered cell.
	tokenRightEdge := (cellW - rightWidth) + costSlotW + 2 + tokenSlotW

	for _, a := range aggs {
		rendered := stripANSI(projectCell(a, cellW))
		// rendered is a single line of exactly cellW visual columns.
		if len(rendered) < tokenRightEdge+2 {
			t.Fatalf("cell for %q too short: len=%d, need %d", a.Label, len(rendered), tokenRightEdge+2)
		}
		gap := rendered[tokenRightEdge : tokenRightEdge+2]
		if gap != "  " {
			t.Errorf("cell for %q (tokens=%d): expected two-space gap at column %d, got %q\nrendered: %q",
				a.Label, a.Tokens, tokenRightEdge, gap, rendered)
		}
	}
}
