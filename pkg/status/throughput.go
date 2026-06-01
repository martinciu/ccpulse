package status

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// throughputWindow is the rolling span over which the live token/cost rate is
// measured. Fixed at 30 minutes — no config knob (see #387).
const throughputWindow = 30 * time.Minute

// throughputWindowMinutes is throughputWindow in minutes, emitted as the JSON
// window_minutes field and used to derive the extrapolation factor. Kept beside
// throughputWindow so the emitted value and the math share one source of truth.
const throughputWindowMinutes = 30

// throughputExtrapolate scales a window total up to a per-hour rate
// (60 / window_minutes). For the fixed 30-minute window this is exactly 2; the
// integer division is exact because 30 divides 60.
const throughputExtrapolate = 60 / throughputWindowMinutes

// throughputSQL sums the headline (input + output) tokens and cost over the
// rolling window in a single indexed scan. idx_messages_ts bounds the scan to
// the window's rows — O(window), not O(history). Each SUM is COALESCE'd to 0
// before the addition, so a fully-empty range yields 0, never NULL + NULL. One
// positional param binds: the window cutoff string.
const throughputSQL = `
SELECT
  COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(output_tokens), 0),
  COALESCE(SUM(cost_usd_estimate), 0.0)
FROM messages
WHERE ts >= ?`

// ComputeThroughput measures the live token/cost burn rate over the rolling
// [now-throughputWindow, now) window and linearly extrapolates it to a per-hour
// figure. It is computed only for `status --json` (the TUI never calls it), so
// it adds nothing to the refresh hot path. Costs reflect AutoRecost, which the
// status command runs first.
//
// The window is a pure now-relative offset — no local-tz / calendar boundary
// and no quota anchoring — so unlike ComputePeriods it takes no QuotaInput.
// Idle (no rows in the window) yields a non-nil Throughput with zeroed numbers.
func ComputeThroughput(ctx context.Context, db *sql.DB, now time.Time) (*Throughput, error) {
	cutoff := now.Add(-throughputWindow).UTC().Format(tsFormat)

	var tokensInWindow int64
	var costInWindow float64
	if err := db.QueryRowContext(ctx, throughputSQL, cutoff).Scan(
		&tokensInWindow, &costInWindow,
	); err != nil {
		return nil, fmt.Errorf("compute throughput: %w", err)
	}

	return &Throughput{
		WindowMinutes:   throughputWindowMinutes,
		TokensInWindow:  tokensInWindow,
		TokensPerHour:   tokensInWindow * throughputExtrapolate,
		CostInWindowUSD: costInWindow,
		CostPerHourUSD:  costInWindow * throughputExtrapolate,
	}, nil
}
