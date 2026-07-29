package cache

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/martinciu/ccpulse/pkg/models"
)

// ModelAggregate is one model's cost/token rollup over a time window, with
// dated release variants folded into the canonical id (models.Canonical).
// Model is "" for the synthetic "unknown model" bucket.
type ModelAggregate struct {
	Model   string // models.Canonical of the raw id; "" = unknown
	Label   string // models.Label(Model)
	CostUSD float64
	Tokens  int64   // SUM(input_tokens + output_tokens) — matches IOTokenBuckets
	CostPct float64 // share of total window cost, 0..100
}

// ModelAggregates returns per-model cost and token totals for messages in
// [from, to). The SUM expressions are identical to ProjectAggregates (and so to
// CostBuckets / IOTokenBuckets), which is what makes the models box reconcile
// with the projects box and with the chart bars for the same window.
//
// SQL groups by the RAW model id; the fold onto models.Canonical happens in Go.
// Expressing the date-strip in SQLite string functions would fork the rule into
// a second, untestable copy that silently desyncs from pkg/models the first
// time it changes. Distinct model ids number in the low tens, so the extra rows
// crossing the driver boundary cost nothing measurable.
//
// Rows are sorted by cost descending, with label then canonical id as
// tiebreaks. The third key is not redundant: the fold runs through a map, whose
// iteration order Go randomises per run, so without a total order two equal-cost
// rows would swap between refreshes and make the box flicker.
//
// The "unknown model" bucket (empty model) is forced last regardless of cost,
// mirroring ProjectAggregates' "(no project)". Models absent from pricing.json
// are NOT dropped — they carry real token counts at zero cost, and filtering
// them would make this box's token total silently disagree with the projects box.
func (c *Cache) ModelAggregates(ctx context.Context, from, to time.Time) ([]ModelAggregate, error) {
	const q = `
SELECT model,
       COALESCE(SUM(cost_usd_estimate), 0)            AS cost,
       COALESCE(SUM(input_tokens + output_tokens), 0) AS tokens
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY model`
	rows, err := c.db.QueryContext(ctx, q,
		from.UTC().Format(tsFormat), to.UTC().Format(tsFormat))
	if err != nil {
		return nil, fmt.Errorf("model aggregates: query: %w", err)
	}
	defer rows.Close()

	byModel := make(map[string]*ModelAggregate)
	var total float64
	for rows.Next() {
		var (
			raw    string
			cost   float64
			tokens int64
		)
		if err := rows.Scan(&raw, &cost, &tokens); err != nil {
			return nil, fmt.Errorf("model aggregates: scan: %w", err)
		}
		key := models.Canonical(raw)
		a, ok := byModel[key]
		if !ok {
			a = &ModelAggregate{Model: key, Label: models.Label(key)}
			byModel[key] = a
		}
		a.CostUSD += cost
		a.Tokens += tokens
		total += cost
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("model aggregates: iterate: %w", err)
	}

	out := make([]ModelAggregate, 0, len(byModel))
	for _, a := range byModel {
		if total > 0 {
			a.CostPct = a.CostUSD / total * 100
		}
		out = append(out, *a)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := out[i].Model == "", out[j].Model == ""
		if ei != ej {
			return ej // a real model sorts before the unknown bucket
		}
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}
