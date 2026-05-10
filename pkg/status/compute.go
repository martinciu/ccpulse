package status

import (
	"database/sql"
	"math"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
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

// Compute folds the last 5 hours of messages plus the quota input into a
// Window. The DB query is unchanged from before — only the tail logic
// (Percent, MinutesToReset) shifts depending on whether quota is available.
func Compute(db *sql.DB, now time.Time, q QuotaInput) (Window, error) {
	cutoff := now.UTC().Add(-5 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
	row := db.QueryRow(`
SELECT
  COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens
              + cache_write_5m_tokens + cache_write_1h_tokens), 0),
  COALESCE(SUM(cost_usd_estimate), 0),
  COALESCE(MIN(ts), '')
FROM messages WHERE ts >= ?`, cutoff)

	var tokens int64
	var cost float64
	var oldest string
	if err := row.Scan(&tokens, &cost, &oldest); err != nil {
		return Window{}, err
	}

	w := Window{
		Tokens5h:      tokens,
		Cost5hUSD:     cost,
		CeilingLabel:  q.TierSlug,
		CeilingPretty: q.TierPretty,
	}

	if q.Usage != nil && q.Usage.FiveHour != nil {
		w.Percent = clampPct(int(math.Round(q.Usage.FiveHour.Utilization)))
		mins := int(q.Usage.FiveHour.ResetsAt.Sub(now).Minutes())
		if mins < 0 {
			mins = 0
		}
		w.MinutesToReset = mins
	} else if oldest != "" {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z07:00", oldest)
		mins := int(t.Add(5 * time.Hour).Sub(now).Minutes())
		if mins < 0 {
			mins = 0
		}
		w.MinutesToReset = mins
	}

	if q.Usage != nil && q.Usage.SevenDay != nil {
		w.Has7d = true
		w.Percent7d = clampPct(int(math.Round(q.Usage.SevenDay.Utilization)))
		mins := int(q.Usage.SevenDay.ResetsAt.Sub(now).Minutes())
		if mins < 0 {
			mins = 0
		}
		w.MinutesToReset7d = mins
	}

	if q.Usage != nil {
		w.Quota = q.Usage
		w.QuotaSource = q.Source
		w.QuotaUpdatedAt = q.UpdatedAt

		var p Projections
		if q.Usage.FiveHour != nil {
			fh := projectBucket(
				q.Usage.FiveHour.Utilization,
				q.Usage.FiveHour.ResetsAt,
				now,
				fiveHourWindow,
				fiveHourLowConfidenceCutoff,
			)
			p.FiveHour = &fh
		}
		if q.Usage.SevenDay != nil {
			sd := projectBucket(
				q.Usage.SevenDay.Utilization,
				q.Usage.SevenDay.ResetsAt,
				now,
				sevenDayWindow,
				sevenDayLowConfidenceCutoff,
			)
			p.SevenDay = &sd
		}
		if p.FiveHour != nil || p.SevenDay != nil {
			w.Projection = &p
		}
	}
	return w, nil
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
