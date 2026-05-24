// Package status computes the 5-hour rolling and 7-day quota windows and maps plan tier to token ceiling.
package status

import (
	"encoding/json"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// Window is the snapshot consumed by the TUI header and `status --json`.
// MinutesToReset and MinutesToReset7d are *int so the "no reset target"
// case (5h idle, 7d upstream glitch) round-trips faithfully through
// `status --json` instead of collapsing to an ambiguous 0.
type Window struct {
	Percent           int               `json:"percent"`
	MinutesToReset    *int              `json:"minutes_to_reset"`
	Percent7d         int               `json:"percent_7d,omitempty"`
	MinutesToReset7d  *int              `json:"minutes_to_reset_7d,omitempty"`
	Has7d             bool              `json:"has_7d,omitempty"`
	CeilingLabel      string            `json:"ceiling_label"`
	CeilingPretty     string            `json:"ceiling_pretty"`
	Tokens5h          int64             `json:"tokens_5h"`
	Tokens5hBreakdown Tokens5hBreakdown `json:"tokens_5h_breakdown"`
	Cost5hUSD         float64           `json:"cost_5h_usd"`
	Quota             *anthro.Usage     `json:"quota,omitempty"`
	QuotaSource       string            `json:"quota_source,omitempty"`
	QuotaUpdatedAt    time.Time         `json:"quota_updated_at,omitzero"`
	Projection        *Projections      `json:"projection,omitempty"`
}

// Projections carries the per-bucket burn-rate predictions.
// Each field is omitted when the corresponding Usage bucket is nil.
type Projections struct {
	FiveHour *Projection `json:"five_hour,omitempty"`
	SevenDay *Projection `json:"seven_day,omitempty"`
}

// Projection is the predicted trajectory for a single quota bucket.
// Numbers are always populated; consumers can branch on Confidence.
type Projection struct {
	ElapsedMinutes      int     `json:"elapsed_minutes"`
	SlopePctPerHour     float64 `json:"slope_pct_per_hour"`
	ProjectedPctAtReset int     `json:"projected_pct_at_reset"`
	WillOverreach       bool    `json:"will_overreach"`
	MinutesTo100Pct     *int    `json:"minutes_to_100_pct"`
	Confidence          string  `json:"confidence"`
}

// Tokens5hBreakdown carries the five-way split of `Tokens5h` across the
// token kinds Anthropic bills separately. `Tokens5h` is `Input + Output`
// alone — the cache fields are exposed here so `status --json` consumers
// can still inspect the cache-vs-work ratio without re-querying.
type Tokens5hBreakdown struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// JSON serializes w to a compact JSON string for the status --json subcommand.
func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}
