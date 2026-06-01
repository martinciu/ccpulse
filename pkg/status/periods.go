package status

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// tsFormat is the layout messages.ts is stored in (UTC). Identical to the
// layout the 5h query in compute.go compares against.
const tsFormat = "2006-01-02T15:04:05.000Z07:00"

// The 7-day quota-window span is sevenDayWindow, declared in projection.go.

// periodsSQL aggregates the five token kinds + cost over the today / 7d / 30d
// windows in a single indexed scan. The inner select tags each row with 1/0
// window-membership flags (SQLite comparisons yield integers); the outer
// SUM(flag * column) folds only the matching rows. 30d is always the widest
// window, so its cutoff bounds the scan and needs no flag. Three positional
// params bind, in order: today cutoff, 7d cutoff, 30d cutoff.
const periodsSQL = `
SELECT
  COALESCE(SUM(in_today * input_tokens), 0),
  COALESCE(SUM(in_today * output_tokens), 0),
  COALESCE(SUM(in_today * cache_read_tokens), 0),
  COALESCE(SUM(in_today * cache_write_5m_tokens), 0),
  COALESCE(SUM(in_today * cache_write_1h_tokens), 0),
  COALESCE(SUM(in_today * cost_usd_estimate), 0.0),
  COALESCE(SUM(in_seven * input_tokens), 0),
  COALESCE(SUM(in_seven * output_tokens), 0),
  COALESCE(SUM(in_seven * cache_read_tokens), 0),
  COALESCE(SUM(in_seven * cache_write_5m_tokens), 0),
  COALESCE(SUM(in_seven * cache_write_1h_tokens), 0),
  COALESCE(SUM(in_seven * cost_usd_estimate), 0.0),
  COALESCE(SUM(input_tokens), 0),
  COALESCE(SUM(output_tokens), 0),
  COALESCE(SUM(cache_read_tokens), 0),
  COALESCE(SUM(cache_write_5m_tokens), 0),
  COALESCE(SUM(cache_write_1h_tokens), 0),
  COALESCE(SUM(cost_usd_estimate), 0.0)
FROM (
  SELECT
    input_tokens, output_tokens, cache_read_tokens,
    cache_write_5m_tokens, cache_write_1h_tokens, cost_usd_estimate,
    (ts >= ?) AS in_today,
    (ts >= ?) AS in_seven
  FROM messages
  WHERE ts >= ?
)`

// ComputePeriods aggregates token usage and cost over the today / 7d / 30d
// trailing windows from the messages table. It is computed only for
// `status --json` (the TUI never calls it), so it adds nothing to the refresh
// hot path. Costs reflect AutoRecost, which the status command runs first.
//
// Windows (all lower bounds derived in local tz, compared as UTC strings):
//
//	today : [DayStartLocal(now), now)
//	7d    : quota-anchored [ResetsAt-168h, now) when the API reports a reset
//	        within the next 7 days; else calendar [DayStartLocal(now)-6d, now)
//	30d   : [DayStartLocal(now)-29d, now)
func ComputePeriods(ctx context.Context, db *sql.DB, now time.Time, q QuotaInput) (*Periods, error) {
	todayStart := cache.DayStartLocal(now)
	thirtyStart := todayStart.AddDate(0, 0, -29)
	sevenStart := sevenDayStart(q, now)

	row := db.QueryRowContext(ctx, periodsSQL,
		todayStart.UTC().Format(tsFormat),
		sevenStart.UTC().Format(tsFormat),
		thirtyStart.UTC().Format(tsFormat),
	)

	var p Periods
	if err := row.Scan(
		&p.Today.TokensBreakdown.Input, &p.Today.TokensBreakdown.Output,
		&p.Today.TokensBreakdown.CacheRead, &p.Today.TokensBreakdown.CacheWrite5m,
		&p.Today.TokensBreakdown.CacheWrite1h, &p.Today.CostUSD,
		&p.SevenDay.TokensBreakdown.Input, &p.SevenDay.TokensBreakdown.Output,
		&p.SevenDay.TokensBreakdown.CacheRead, &p.SevenDay.TokensBreakdown.CacheWrite5m,
		&p.SevenDay.TokensBreakdown.CacheWrite1h, &p.SevenDay.CostUSD,
		&p.ThirtyDay.TokensBreakdown.Input, &p.ThirtyDay.TokensBreakdown.Output,
		&p.ThirtyDay.TokensBreakdown.CacheRead, &p.ThirtyDay.TokensBreakdown.CacheWrite5m,
		&p.ThirtyDay.TokensBreakdown.CacheWrite1h, &p.ThirtyDay.CostUSD,
	); err != nil {
		return nil, fmt.Errorf("compute periods: %w", err)
	}

	p.Today.Tokens = p.Today.TokensBreakdown.Input + p.Today.TokensBreakdown.Output
	p.SevenDay.Tokens = p.SevenDay.TokensBreakdown.Input + p.SevenDay.TokensBreakdown.Output
	p.ThirtyDay.Tokens = p.ThirtyDay.TokensBreakdown.Input + p.ThirtyDay.TokensBreakdown.Output

	return &p, nil
}

// sevenDayStart returns the lower bound of the 7d window. Task 3 adds the
// quota-anchored branch; for now it always returns the calendar window: the
// last 7 calendar days in local tz (today + previous 6).
func sevenDayStart(q QuotaInput, now time.Time) time.Time {
	return cache.DayStartLocal(now).AddDate(0, 0, -6)
}
