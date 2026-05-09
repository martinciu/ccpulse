package status

import (
	"database/sql"
	"time"
)

// Compute folds the last 5 hours of messages into a Window. ceilingTokens
// of 0 means "raw totals (api tier)" → Percent stays 0.
func Compute(db *sql.DB, now time.Time, ceilingLabel string, ceilingTokens int64) (Window, error) {
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
		Tokens5h:     tokens,
		Cost5hUSD:    cost,
		CeilingLabel: ceilingLabel,
	}
	if oldest != "" {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z07:00", oldest)
		reset := t.Add(5 * time.Hour)
		mins := int(reset.Sub(now).Minutes())
		if mins < 0 {
			mins = 0
		}
		w.MinutesToReset = mins
	}
	if ceilingTokens > 0 {
		w.Percent = int(float64(tokens) / float64(ceilingTokens) * 100)
		if w.Percent > 100 {
			w.Percent = 100
		}
	}
	return w, nil
}
