package tui

import (
	"fmt"
	"strings"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderToday(rows []cache.ModelTotals) string {
	if len(rows) == 0 {
		return "  No usage today."
	}
	var b strings.Builder
	b.WriteString("  Today — per model\n")
	b.WriteString("  ──────────────────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  %-25s %6s %10s %10s %10s %10s %8s\n",
		"model", "msgs", "input", "output", "cache-r", "cache-w", "cost"))
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-25s %6d %10d %10d %10d %10d %s\n",
			r.Model, r.Messages, r.Input, r.Output, r.CacheRead, r.CacheWrite,
			fmt.Sprintf("$%.2f", r.CostUSD)))
	}
	return b.String()
}
