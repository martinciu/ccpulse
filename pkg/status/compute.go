package status

import (
	"database/sql"
	"log/slog"
	"math"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// QuotaInput carries server-side quota data into Compute.
// Usage == nil → fall back to the JSONL heuristic for MinutesToReset and
// leave Percent at 0.
type QuotaInput struct {
	Usage      *anthro.Usage
	Source     string
	UpdatedAt  time.Time
	TierSlug   string
	TierPretty string
}

// Compute folds the last 5 hours of messages plus the quota input into
// a Window. `Tokens5h` is `input + output` only — see #232 for why this
// matches Claude Code `/usage`. `Tokens5hBreakdown` exposes all five
// token kinds for callers that still need the cache-vs-work split.
func Compute(db *sql.DB, now time.Time, q QuotaInput) (Window, error) {
	cutoff := now.UTC().Add(-5 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
	row := db.QueryRow(`
SELECT
  COALESCE(SUM(input_tokens), 0),
  COALESCE(SUM(output_tokens), 0),
  COALESCE(SUM(cache_read_tokens), 0),
  COALESCE(SUM(cache_write_5m_tokens), 0),
  COALESCE(SUM(cache_write_1h_tokens), 0),
  COALESCE(SUM(cost_usd_estimate), 0),
  COALESCE(MIN(ts), '')
FROM messages WHERE ts >= ?`, cutoff)

	var b Tokens5hBreakdown
	var cost float64
	var oldest string
	if err := row.Scan(
		&b.Input, &b.Output, &b.CacheRead,
		&b.CacheWrite5m, &b.CacheWrite1h,
		&cost, &oldest,
	); err != nil {
		return Window{}, err
	}

	w := Window{
		Tokens5h:          b.Input + b.Output,
		Tokens5hBreakdown: b,
		Cost5hUSD:         cost,
		CeilingLabel:      q.TierSlug,
		CeilingPretty:     q.TierPretty,
	}

	w.Percent, w.MinutesToReset = resolveFiveHour(q, oldest, now)

	if q.Usage != nil && q.Usage.SevenDay != nil && q.Usage.SevenDay.ResetsAt != nil {
		w.Has7d = true
		w.Percent7d = clampPct(int(math.Round(q.Usage.SevenDay.Utilization)))
		mins := max(int(q.Usage.SevenDay.ResetsAt.Sub(now).Minutes()), 0)
		w.MinutesToReset7d = &mins
	}

	if q.Usage != nil {
		w.Quota = q.Usage
		w.QuotaSource = q.Source
		w.QuotaUpdatedAt = q.UpdatedAt
		w.Projection = buildProjections(db, q.Usage, now)
	}
	return w, nil
}

// resolveFiveHour computes the 5h Percent and MinutesToReset, falling back to
// the oldest-message heuristic when the API reports no reset boundary.
func resolveFiveHour(q QuotaInput, oldest string, now time.Time) (percent int, minutesToReset *int) {
	switch {
	case q.Usage != nil && q.Usage.FiveHour != nil && q.Usage.FiveHour.ResetsAt != nil:
		percent = clampPct(int(math.Round(q.Usage.FiveHour.Utilization)))
		mins := max(int(q.Usage.FiveHour.ResetsAt.Sub(now).Minutes()), 0)
		minutesToReset = &mins
	case q.Usage != nil && q.Usage.FiveHour != nil:
		// 5h bucket present but ResetsAt nil → idle window. Carry the (zero)
		// Percent through but leave MinutesToReset nil so callers can render
		// "idle" rather than a misleading 0.
		percent = clampPct(int(math.Round(q.Usage.FiveHour.Utilization)))
	case oldest != "":
		t, _ := time.Parse("2006-01-02T15:04:05.000Z07:00", oldest)
		mins := max(int(t.Add(5*time.Hour).Sub(now).Minutes()), 0)
		minutesToReset = &mins
	}
	return percent, minutesToReset
}

// loadSevenDaySamples fetches the trailing 7-day utilisation samples used to
// derive a measured slope. Returns nil (linear fallback) when db is nil or the
// query fails.
func loadSevenDaySamples(db *sql.DB, now time.Time) []cache.SevenDaySample {
	if db == nil {
		return nil
	}
	cc := cache.NewFromDB(db)
	samples, err := cc.SevenDaySamplesSince(now.Add(-sevenDayTrailingWindow))
	if err != nil {
		slog.Debug("status.Compute: SevenDaySamplesSince failed; falling back to linear",
			"err", err)
		return nil
	}
	return samples
}

// buildProjections derives the per-bucket burn-rate predictions from the usage
// snapshot. Returns nil when neither bucket can be projected.
func buildProjections(db *sql.DB, usage *anthro.Usage, now time.Time) *Projections {
	if usage == nil {
		return nil
	}
	var p Projections
	if usage.FiveHour != nil {
		fh := projectBucket(
			usage.FiveHour.Utilization,
			usage.FiveHour.ResetsAt,
			now,
			fiveHourWindow,
			fiveHourLowConfidenceCutoff,
		)
		p.FiveHour = &fh
	}
	if usage.SevenDay != nil && usage.SevenDay.ResetsAt != nil {
		sd := projectSevenDay(
			loadSevenDaySamples(db, now),
			usage.SevenDay.Utilization,
			usage.SevenDay.ResetsAt,
			now,
		)
		p.SevenDay = &sd
	}
	if p.FiveHour == nil && p.SevenDay == nil {
		return nil
	}
	return &p
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
