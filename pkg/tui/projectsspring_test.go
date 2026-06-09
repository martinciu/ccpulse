package tui

import (
	"testing"
	"time"
)

func TestLerpInt(t *testing.T) {
	cases := []struct {
		a, b int
		r    float64
		want int
	}{
		{0, 12, 0, 0},
		{0, 12, 1, 12},
		{0, 12, 0.5, 6},
		{12, 0, 0.5, 6},
		{12, 0, 1, 0},
		{0, 10, 0.24, 2}, // 2.4 rounds to 2
		{0, 10, 0.25, 3}, // 2.5 rounds to 3 (math.Round)
	}
	for _, c := range cases {
		if got := lerpInt(c.a, c.b, c.r); got != c.want {
			t.Errorf("lerpInt(%d,%d,%g)=%d, want %d", c.a, c.b, c.r, got, c.want)
		}
	}
}

func TestProjectsHeight_SpringBranchOverridesTarget(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()

	// Steady target (122x40 → m.h-7=33 → min(16,12)=12).
	m.showProjects = true
	if got, want := m.projectsHeight(), m.projectsTargetHeight(); got != want {
		t.Fatalf("steady projectsHeight()=%d, want projectsTargetHeight()=%d", got, want)
	}
	if m.projectsTargetHeight() != 12 {
		t.Fatalf("projectsTargetHeight()=%d, want 12 at 122x40", m.projectsTargetHeight())
	}

	// Spring branch: returns projectsAnimH regardless of showProjects.
	m.springActive = true
	m.springKind = springKindProjects
	m.projectsAnimH = 7
	if got := m.projectsHeight(); got != 7 {
		t.Errorf("in-slide projectsHeight()=%d, want 7 (animated)", got)
	}
	m.showProjects = false
	if got := m.projectsHeight(); got != 7 {
		t.Errorf("in-slide projectsHeight() with showProjects=false=%d, want 7", got)
	}
}
