package tui

import (
	"fmt"
	"strings"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderHistory(rows []cache.DayTotals) string {
	if len(rows) == 0 {
		return "  No history."
	}
	var b strings.Builder
	b.WriteString("  History — last 30 days\n")
	b.WriteString("  ──────────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  %-12s %10s %14s %10s\n", "date", "sessions", "tokens", "cost"))
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-12s %10d %14d $%6.2f\n",
			r.Date, r.Sessions, r.Tokens, r.CostUSD))
	}
	return b.String()
}
