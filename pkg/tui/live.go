package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderLive(sessions []cache.LiveSession, thisTmux map[string]bool, now time.Time) string {
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
		marker := liveMarker(s, thisTmux, now)
		fmt.Fprintf(&b, "  %s %-30s %-15s $%.2f\n", marker, proj, s.Model, s.CostUSD)
	}
	return b.String()
}

// liveMarker returns the recency/this-tmux marker for a row.
// Possible outputs: "  " (blank), "⚡ ", "◆ ", "⚡◆".
func liveMarker(s cache.LiveSession, thisTmux map[string]bool, now time.Time) string {
	recent := now.Sub(s.LastTS) < 10*time.Minute
	mine := thisTmux[s.SessionID]
	switch {
	case recent && mine:
		return "⚡◆"
	case recent:
		return "⚡ "
	case mine:
		return "◆ "
	default:
		return "  "
	}
}
