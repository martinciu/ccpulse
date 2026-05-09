package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderLive(sessions []cache.LiveSession) string {
	if len(sessions) == 0 {
		return "  No active sessions in the last 24h."
	}
	var b strings.Builder
	b.WriteString("  Active sessions\n")
	b.WriteString("  ─────────────────────────────────────────────────────────\n")
	for _, s := range sessions {
		proj := filepath.Base(s.ProjectCanonical)
		if s.WorktreeBranch != "" {
			proj += "/" + s.WorktreeBranch
		}
		fmt.Fprintf(&b, "  %-30s %-15s $%.2f\n", proj, s.Model, s.CostUSD)
	}
	return b.String()
}
