package tui

import (
	"fmt"
	"strings"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func renderModels(rows []cache.ModelTotals, window cache.ModelsWindow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Models — window: %s\n", window)
	b.WriteString("  ────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  %-25s %6s %10s %10s %8s\n",
		"model", "msgs", "input", "output", "cost"))
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("  %-25s %6d %10d %10d $%6.2f\n",
			r.Model, r.Messages, r.Input, r.Output, r.CostUSD))
	}
	return b.String()
}
