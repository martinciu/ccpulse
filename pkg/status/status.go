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
	Percent           int             `json:"percent"`
	MinutesToReset    *int            `json:"minutes_to_reset"`
	Percent7d         int             `json:"percent_7d,omitempty"`
	MinutesToReset7d  *int            `json:"minutes_to_reset_7d,omitempty"`
	Has7d             bool            `json:"has_7d,omitempty"`
	CeilingLabel      string          `json:"ceiling_label"`
	CeilingPretty     string          `json:"ceiling_pretty"`
	Tokens5h          int64           `json:"tokens_5h"`
	Tokens5hBreakdown TokensBreakdown `json:"tokens_5h_breakdown"`
	Cost5hUSD         float64         `json:"cost_5h_usd"`
	Quota             *anthro.Usage   `json:"quota,omitempty"`
	QuotaSource       string          `json:"quota_source,omitempty"`
	QuotaUpdatedAt    time.Time       `json:"quota_updated_at,omitzero"`
	Projection        *Projections    `json:"projection,omitempty"`
	Periods           *Periods        `json:"periods,omitempty"`
	Throughput        *Throughput     `json:"throughput,omitempty"`
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

// TokensBreakdown carries the five-way split of a token total across the
// token kinds Anthropic bills separately. The headline figure (`Tokens5h`,
// or `Period.Tokens`) is `Input + Output` alone — the cache fields are
// exposed here so `status --json` consumers can still inspect the
// cache-vs-work ratio without re-querying.
type TokensBreakdown struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Period is one trailing-window usage rollup: Tokens (== breakdown
// Input + Output), the five-way TokensBreakdown, and CostUSD.
type Period struct {
	Tokens          int64           `json:"tokens"`
	TokensBreakdown TokensBreakdown `json:"tokens_breakdown"`
	CostUSD         float64         `json:"cost_usd"`
}

// Periods carries the today / 7d / 30d usage rollups for `status --json`.
// Populated only on the CLI JSON path (ComputePeriods); nil on the TUI
// compute path, where `omitempty` drops it from serialization.
type Periods struct {
	Today     Period `json:"today"`
	SevenDay  Period `json:"7d"`
	ThirtyDay Period `json:"30d"`
}

// Throughput is the live token/cost burn rate over a rolling window, emitted
// only on `status --json`. The per-hour figures are the in-window totals scaled
// by 60/WindowMinutes (== 2 for the fixed 30-minute window). Tokens are the
// headline Input + Output only, matching Tokens5h / Period.Tokens. Populated
// only on the CLI JSON path (ComputeThroughput); nil on the TUI compute path,
// where omitempty drops it from serialization. Idle (no activity in the window)
// is an honest zero, never null.
type Throughput struct {
	WindowMinutes   int     `json:"window_minutes"`
	TokensInWindow  int64   `json:"tokens_in_window"`
	TokensPerHour   int64   `json:"tokens_per_hour"`
	CostInWindowUSD float64 `json:"cost_in_window_usd"`
	CostPerHourUSD  float64 `json:"cost_per_hour_usd"`
}

// JSON serializes w to a compact JSON string for the status --json subcommand.
func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}
