package status

import (
	"encoding/json"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// Window is the snapshot consumed by the TUI header and `status --json`.
type Window struct {
	Percent          int           `json:"percent"`
	MinutesToReset   int           `json:"minutes_to_reset"`
	Percent7d        int           `json:"percent_7d,omitempty"`
	MinutesToReset7d int           `json:"minutes_to_reset_7d,omitempty"`
	Has7d            bool          `json:"has_7d,omitempty"`
	CeilingLabel     string        `json:"ceiling_label"`
	CeilingPretty    string        `json:"ceiling_pretty"`
	Tokens5h         int64         `json:"tokens_5h"`
	Cost5hUSD        float64       `json:"cost_5h_usd"`
	Quota            *anthro.Usage `json:"quota,omitempty"`
	QuotaSource      string        `json:"quota_source,omitempty"`
	QuotaUpdatedAt   time.Time     `json:"quota_updated_at,omitzero"`
}

func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}