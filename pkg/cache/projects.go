package cache

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/martinciu/ccpulse/pkg/projects"
)

// ProjectAggregate is one project's cost/token rollup over a time window,
// with worktrees and subdirectories folded into the parent repo (via
// repo_root). RepoRoot is "" for the synthetic "no project" bucket.
type ProjectAggregate struct {
	RepoRoot string
	Label    string // projects.LabelFromRoot(RepoRoot)
	CostUSD  float64
	Tokens   int64   // SUM(input_tokens + output_tokens) — matches IOTokenBuckets
	CostPct  float64 // share of total window cost, 0..100
}

// ProjectAggregates returns per-project cost and token totals for messages
// in [from, to), grouped by repo_root. The SUM expressions are identical to
// CostBuckets (cost_usd_estimate) and IOTokenBuckets (input+output), so the
// table reconciles with the chart bars. Rows are sorted by cost descending
// (alphabetical tiebreak); the "no project" bucket (empty repo_root) is
// forced last regardless of cost.
func (c *Cache) ProjectAggregates(ctx context.Context, from, to time.Time) ([]ProjectAggregate, error) {
	const q = `
SELECT repo_root,
       COALESCE(SUM(cost_usd_estimate), 0)            AS cost,
       COALESCE(SUM(input_tokens + output_tokens), 0) AS tokens
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY repo_root`
	rows, err := c.db.QueryContext(ctx, q,
		from.UTC().Format(tsFormat), to.UTC().Format(tsFormat))
	if err != nil {
		return nil, fmt.Errorf("project aggregates: query: %w", err)
	}
	defer rows.Close()

	var (
		out   []ProjectAggregate
		total float64
	)
	for rows.Next() {
		var a ProjectAggregate
		if err := rows.Scan(&a.RepoRoot, &a.CostUSD, &a.Tokens); err != nil {
			return nil, fmt.Errorf("project aggregates: scan: %w", err)
		}
		a.Label = projects.LabelFromRoot(a.RepoRoot)
		total += a.CostUSD
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project aggregates: iterate: %w", err)
	}

	for i := range out {
		if total > 0 {
			out[i].CostPct = out[i].CostUSD / total * 100
		}
	}

	// Sort cost desc, alphabetical tiebreak, "no project" (empty root) last.
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := out[i].RepoRoot == "", out[j].RepoRoot == ""
		if ei != ej {
			return ej // a real project sorts before the no-project bucket
		}
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}
