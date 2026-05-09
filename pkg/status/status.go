package status

import (
	"encoding/json"
	"fmt"

	"github.com/martinciu/ccpulse/pkg/config"
)

type Window struct {
	Percent        int     `json:"percent"`
	MinutesToReset int     `json:"minutes_to_reset"`
	CeilingLabel   string  `json:"ceiling_label"`
	Tokens5h       int64   `json:"tokens_5h"`
	Cost5hUSD      float64 `json:"cost_5h_usd"`
}

func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}

const (
	clrViolet = "#6c71c4"
	clrYellow = "#b58900"
	clrRed    = "#dc322f"
)

const speedometer = "" // U+F490 (Nerd Font speedometer)

func TmuxLine(w Window, p config.Plan) string {
	if p.Tier == "api" {
		color := bucketColorByCost(w.Cost5hUSD, p.APIWarnUSD, p.APIHotUSD)
		return fmt.Sprintf("#[fg=%s]%s $%.2f • %s", color, speedometer, w.Cost5hUSD, dur(w.MinutesToReset))
	}
	color := bucketColorByPercent(w.Percent)
	return fmt.Sprintf("#[fg=%s]%s %d%% • %s", color, speedometer, w.Percent, dur(w.MinutesToReset))
}

func bucketColorByPercent(p int) string {
	switch {
	case p >= 90:
		return clrRed
	case p >= 70:
		return clrYellow
	}
	return clrViolet
}

func bucketColorByCost(cost, warn, hot float64) string {
	if hot == 0 {
		hot = 10.0
	}
	if warn == 0 {
		warn = 5.0
	}
	switch {
	case cost >= hot:
		return clrRed
	case cost >= warn:
		return clrYellow
	}
	return clrViolet
}

func dur(mins int) string {
	if mins >= 60 {
		return fmt.Sprintf("%dh%dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}

// CeilingFor returns the rough token-budget ceiling for the user's plan
// tier. These are best-effort approximations — Anthropic doesn't publish
// exact caps. Use tier="custom" with cfg.Plan.CustomCeilingTokens for
// precise values calibrated from observed rate-limit behaviour.
func CeilingFor(p config.Plan) int64 {
	switch p.Tier {
	case "custom":
		return p.CustomCeilingTokens
	case "max_5x":
		return 60_000_000
	case "max_20x":
		return 240_000_000
	case "pro":
		return 12_000_000
	case "api":
		return 0
	}
	return 0
}
