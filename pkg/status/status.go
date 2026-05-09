package status

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// Window is the snapshot consumed by the TUI header, the tmux line,
// and `status --json`.
type Window struct {
	Percent        int           `json:"percent"`
	MinutesToReset int           `json:"minutes_to_reset"`
	CeilingLabel   string        `json:"ceiling_label"`
	CeilingPretty  string        `json:"ceiling_pretty"`
	Tokens5h       int64         `json:"tokens_5h"`
	Cost5hUSD      float64       `json:"cost_5h_usd"`
	Quota          *anthro.Usage `json:"quota,omitempty"`
	QuotaSource    string        `json:"quota_source,omitempty"`
	QuotaUpdatedAt time.Time     `json:"quota_updated_at,omitzero"`
}

func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}

// DisplayMode controls whether the tmux line shows percent or cost.
type DisplayMode int

const (
	DisplayPercent DisplayMode = iota
	DisplayCost
)

// DisplayBudget carries the cost-mode color thresholds.
type DisplayBudget struct {
	WarnUSD float64
	HotUSD  float64
}

const (
	clrViolet = "#6c71c4"
	clrYellow = "#b58900"
	clrRed    = "#dc322f"
)

const speedometer = "" // U+F490 (Nerd Font speedometer)

func TmuxLine(w Window, mode DisplayMode, b DisplayBudget) string {
	if mode == DisplayCost {
		warn, hot := b.WarnUSD, b.HotUSD
		if hot == 0 {
			hot = 10.0
		}
		if warn == 0 {
			warn = 5.0
		}
		color := bucketColorByCost(w.Cost5hUSD, warn, hot)
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
