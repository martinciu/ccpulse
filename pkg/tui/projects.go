package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderProjects(rows []cache.ProjectTotals, now time.Time) string {
	if len(rows) == 0 {
		return "  No projects."
	}
	var b strings.Builder
	b.WriteString("  Projects — last 30 days\n")
	b.WriteString("  ─────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  %-30s %10s %10s %10s\n", "project", "sessions", "cost", "last"))
	for _, r := range rows {
		ago := now.Sub(r.LastActive).Truncate(time.Minute)
		b.WriteString(fmt.Sprintf("  %-30s %10d $%8.2f %10s\n",
			filepath.Base(r.ProjectCanonical), r.Sessions, r.CostUSD, ago))
	}
	return b.String()
}
