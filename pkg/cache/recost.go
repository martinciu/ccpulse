package cache

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// RecostStats summarizes a Recost run.
type RecostStats struct {
	Scanned       int
	Updated       int
	UnknownBefore int
	UnknownAfter  int
	ByVersion     map[string]int
	Elapsed       time.Duration
}

// RecostOpts modifies Recost behavior. The zero value runs a normal (writing) recost.
type RecostOpts struct {
	DryRun bool
}

const recostBatchSize = 500

type recostUpdate struct {
	id         int64
	newCost    float64
	newVersion string
	newUnknown int
}

// Recost re-resolves pricing_version and cost_usd_estimate for every message
// row against hist. A row is rewritten when the stamped pricing_version disagrees
// with hist.TableAt(ts).Version, the row is pricing_unknown=1 and the resolved
// Table now contains its model, or the recomputed cost differs from stored cost.
//
// Idempotent: re-running on an already-recosted DB reports Updated=0.
// On context cancellation the transaction is rolled back.
func (c *Cache) Recost(ctx context.Context, hist pricing.History, opts RecostOpts) (RecostStats, error) {
	start := time.Now()
	stats := RecostStats{ByVersion: map[string]int{}}

	if err := ctx.Err(); err != nil {
		return stats, fmt.Errorf("recost: ctx: %w", err)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("recost: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, `
SELECT id, ts, model,
       input_tokens, output_tokens, cache_read_tokens,
       cache_write_5m_tokens, cache_write_1h_tokens,
       cost_usd_estimate, pricing_version, pricing_unknown
FROM messages`)
	if err != nil {
		return stats, fmt.Errorf("recost: query messages: %w", err)
	}
	defer rows.Close()

	var batch []recostUpdate

	for rows.Next() {
		var (
			id                  int64
			tsStr               string
			model               string
			in, out, cr         int64
			cw5, cw1            int64
			costStored          float64
			verStored           string
			unkStored           int
		)
		if err := rows.Scan(&id, &tsStr, &model, &in, &out, &cr, &cw5, &cw1, &costStored, &verStored, &unkStored); err != nil {
			return stats, fmt.Errorf("recost: scan row: %w", err)
		}
		stats.Scanned++
		if unkStored == 1 {
			stats.UnknownBefore++
		}
		ts, err := time.Parse(tsFormat, tsStr)
		if err != nil {
			return stats, fmt.Errorf("recost: parse ts %q on row id=%d: %w", tsStr, id, err)
		}
		m := parse.Message{
			Timestamp:          ts,
			Model:              model,
			InputTokens:        in,
			OutputTokens:       out,
			CacheReadTokens:    cr,
			CacheWrite5mTokens: cw5,
			CacheWrite1hTokens: cw1,
		}
		newCost, newVersion, unknown := hist.CostFor(m)
		newUnk := 0
		if unknown {
			newUnk = 1
			stats.UnknownAfter++
		}
		if newVersion == verStored && newUnk == unkStored && newCost == costStored {
			continue
		}
		batch = append(batch, recostUpdate{
			id:         id,
			newCost:    newCost,
			newVersion: newVersion,
			newUnknown: newUnk,
		})
		if len(batch) >= recostBatchSize {
			if err := flushRecostBatch(ctx, tx, batch, opts.DryRun); err != nil {
				return stats, err
			}
			for _, u := range batch {
				stats.Updated++
				stats.ByVersion[u.newVersion]++
			}
			batch = batch[:0]
			if err := ctx.Err(); err != nil {
				return stats, fmt.Errorf("recost: ctx: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("recost: iterate: %w", err)
	}

	if len(batch) > 0 {
		if err := flushRecostBatch(ctx, tx, batch, opts.DryRun); err != nil {
			return stats, err
		}
		for _, u := range batch {
			stats.Updated++
			stats.ByVersion[u.newVersion]++
		}
	}

	if !opts.DryRun {
		if err := tx.Commit(); err != nil {
			return stats, fmt.Errorf("recost: commit: %w", err)
		}
		committed = true
	}
	stats.Elapsed = time.Since(start)
	return stats, nil
}

func flushRecostBatch(ctx context.Context, tx *sql.Tx, batch []recostUpdate, dryRun bool) error {
	if dryRun {
		return nil
	}
	for _, u := range batch {
		if _, err := tx.ExecContext(ctx,
			`UPDATE messages SET cost_usd_estimate = ?, pricing_version = ?, pricing_unknown = ? WHERE id = ?`,
			u.newCost, u.newVersion, u.newUnknown, u.id); err != nil {
			return fmt.Errorf("recost: update row id=%d: %w", u.id, err)
		}
	}
	return nil
}
