package cache

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

const metaKeyRecostFingerprint = "last_recost_history_fingerprint"

// RecostStats summarizes a Recost run.
type RecostStats struct {
	Scanned int
	Updated int
	// Queued is the number of rows that were detected as needing an update and
	// added to the in-flight batch but not yet flushed when Recost returned
	// (relevant on the cancellation/timeout path).
	Queued        int
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
	stats := RecostStats{ByVersion: make(map[string]int, 4)}

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

	if err := c.recostMessages(ctx, tx, hist, opts, &stats); err != nil {
		return stats, err
	}

	if !opts.DryRun {
		fp := strings.Join(hist.Versions(), ",")
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`,
			metaKeyRecostFingerprint, fp); err != nil {
			return stats, fmt.Errorf("recost: write fingerprint: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return stats, fmt.Errorf("recost: commit: %w", err)
		}
		committed = true
	}
	stats.Elapsed = time.Since(start)
	return stats, nil
}

// AutoRecost runs Recost with a bounded timeout and emits a single slog line
// when rows were updated. Intended for command entrypoints that should never
// block the UI on a malformed DB.
//
// It performs a fingerprint early-out: if the meta table already holds a
// fingerprint matching the current hist, Recost is skipped entirely (silent).
func (c *Cache) AutoRecost(ctx context.Context, hist pricing.History) {
	fp := strings.Join(hist.Versions(), ",")
	var stored string
	_ = c.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaKeyRecostFingerprint).Scan(&stored)
	if stored == fp {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stats, err := c.Recost(cctx, hist, RecostOpts{})
	if err != nil {
		slog.Warn("recost",
			"err", err.Error(),
			"scanned", stats.Scanned,
			"updated", stats.Updated,
			"queued", stats.Queued,
			"elapsed", stats.Elapsed,
		)
		return
	}
	if stats.Updated > 0 {
		slog.Info("recost",
			"scanned", stats.Scanned,
			"updated", stats.Updated,
			"unknown_cleared", stats.UnknownBefore-stats.UnknownAfter,
			"elapsed", stats.Elapsed,
		)
	}
}

// PricingVersionStat summarizes one distinct pricing_version stamp present
// in the messages table.
type PricingVersionStat struct {
	Version   string
	Rows      int
	IsCurrent bool
	Stale     int
}

// PricingVersionStats returns one entry per distinct pricing_version found in
// messages, plus a per-version count of rows that disagree with the
// fall-forward-resolved version (hist.VersionFor). Entries are sorted by Version
// ascending.
func (c *Cache) PricingVersionStats(ctx context.Context, hist pricing.History) ([]PricingVersionStat, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT pricing_version, ts, model FROM messages`)
	if err != nil {
		return nil, fmt.Errorf("pricing version stats: query: %w", err)
	}
	defer rows.Close()

	current := hist.Latest().Version
	totals := map[string]int{}
	staleByVer := map[string]int{}
	for rows.Next() {
		var ver, tsStr, model string
		if err := rows.Scan(&ver, &tsStr, &model); err != nil {
			return nil, fmt.Errorf("pricing version stats: scan: %w", err)
		}
		totals[ver]++
		ts, err := time.Parse(tsFormat, tsStr)
		if err != nil {
			continue
		}
		if hist.VersionFor(ts, model) != ver {
			staleByVer[ver]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pricing version stats: iterate: %w", err)
	}

	out := make([]PricingVersionStat, 0, len(totals))
	for ver, n := range totals {
		out = append(out, PricingVersionStat{
			Version:   ver,
			Rows:      n,
			IsCurrent: ver == current,
			Stale:     staleByVer[ver],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// evalRecostRow scans one messages row and resolves whether its stored cost /
// pricing_version / unknown flag still agree with hist. changed reports whether
// the row needs rewriting (upd is only meaningful when changed is true).
// unkBefore/unkAfter are the stored and recomputed unknown flags (0/1) for stats.
func evalRecostRow(rows *sql.Rows, hist pricing.History) (upd recostUpdate, unkBefore, unkAfter int, changed bool, err error) {
	var (
		id          int64
		tsStr       string
		model       string
		in, out, cr int64
		cw5, cw1    int64
		costStored  float64
		verStored   string
		unkStored   int
	)
	if err := rows.Scan(&id, &tsStr, &model, &in, &out, &cr, &cw5, &cw1, &costStored, &verStored, &unkStored); err != nil {
		return recostUpdate{}, 0, 0, false, fmt.Errorf("recost: scan row: %w", err)
	}
	unkBefore = unkStored
	ts, perr := time.Parse(tsFormat, tsStr)
	if perr != nil {
		return recostUpdate{}, unkBefore, 0, false, fmt.Errorf("recost: parse ts on row id=%d: invalid format", id)
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
	}
	unkAfter = newUnk
	if newVersion == verStored && newUnk == unkStored && newCost == costStored {
		return recostUpdate{}, unkBefore, unkAfter, false, nil
	}
	return recostUpdate{id: id, newCost: newCost, newVersion: newVersion, newUnknown: newUnk}, unkBefore, unkAfter, true, nil
}

// flushAndTally writes batch via flushRecostBatch and, on success, folds the rows
// into stats.Updated and stats.ByVersion. On a dry run flushRecostBatch no-ops but
// the tally still runs, so DryRun reports the would-update count.
func flushAndTally(ctx context.Context, stmt *sql.Stmt, batch []recostUpdate, dryRun bool, stats *RecostStats) error {
	if err := flushRecostBatch(ctx, stmt, batch, dryRun); err != nil {
		return err
	}
	for _, u := range batch {
		stats.Updated++
		stats.ByVersion[u.newVersion]++
	}
	return nil
}

// recostMessages streams every messages row through evalRecostRow, batching
// rewrites and flushing them via flushAndTally. It updates stats in place
// (Scanned, UnknownBefore/After, Updated, ByVersion, and Queued on the
// error/cancellation path). The caller owns the transaction lifecycle.
func (c *Cache) recostMessages(ctx context.Context, tx *sql.Tx, hist pricing.History, opts RecostOpts, stats *RecostStats) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id, ts, model,
       input_tokens, output_tokens, cache_read_tokens,
       cache_write_5m_tokens, cache_write_1h_tokens,
       cost_usd_estimate, pricing_version, pricing_unknown
FROM messages`)
	if err != nil {
		return fmt.Errorf("recost: query messages: %w", err)
	}
	defer rows.Close()

	updateStmt, err := tx.PrepareContext(ctx,
		`UPDATE messages SET cost_usd_estimate = ?, pricing_version = ?, pricing_unknown = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("recost: prepare update: %w", err)
	}
	defer updateStmt.Close()

	batch := make([]recostUpdate, 0, recostBatchSize)
	for rows.Next() {
		upd, unkBefore, unkAfter, changed, err := evalRecostRow(rows, hist)
		if err != nil {
			stats.Queued = len(batch)
			return err
		}
		stats.Scanned++
		stats.UnknownBefore += unkBefore
		stats.UnknownAfter += unkAfter
		if !changed {
			continue
		}
		batch = append(batch, upd)
		if len(batch) >= recostBatchSize {
			if err := flushAndTally(ctx, updateStmt, batch, opts.DryRun, stats); err != nil {
				stats.Queued = len(batch)
				return err
			}
			batch = batch[:0]
			if err := ctx.Err(); err != nil {
				// batch is already empty after the flush above; Queued stays 0.
				return fmt.Errorf("recost: ctx: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		stats.Queued = len(batch)
		return fmt.Errorf("recost: iterate: %w", err)
	}
	if len(batch) > 0 {
		if err := flushAndTally(ctx, updateStmt, batch, opts.DryRun, stats); err != nil {
			stats.Queued = len(batch)
			return err
		}
	}
	return nil
}

func flushRecostBatch(ctx context.Context, stmt *sql.Stmt, batch []recostUpdate, dryRun bool) error {
	if dryRun {
		return nil
	}
	for _, u := range batch {
		if _, err := stmt.ExecContext(ctx, u.newCost, u.newVersion, u.newUnknown, u.id); err != nil {
			return fmt.Errorf("recost: update row id=%d: %w", u.id, err)
		}
	}
	return nil
}
