package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkModelAggregates mirrors BenchmarkProjectAggregates: the realistic
// on-screen window (hot path) and the full-table worst case, with matching
// sub-benchmark names (on_screen_32h / full_table) and window sizes so
// benchstat can pair the two across siblings. Its purpose is to confirm the
// Go-side canonical fold does not turn a cheap query into an allocation
// story — including at the high cardinality the fold's justifying doc
// comment (models.go:31-33) assumes stays cheap (see the distinct_200
// sub-benchmark below, ccpulse-475.16).
func BenchmarkModelAggregates(b *testing.B) {
	c, err := Open(context.Background(), filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	ctx := b.Context()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mdls := []string{
		"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5-20251001",
		"claude-haiku-4-5", "claude-fable-5", "gpt-oss:20b", "<synthetic>", "",
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := range 50_000 {
		ts := base.Add(time.Duration(i) * time.Minute).UTC().Format(tsFormat)
		id := fmt.Sprintf("m%d", i)
		if _, err := stmt.ExecContext(ctx, id, id, "slug", ts, "assistant", mdls[i%len(mdls)],
			100, 200, 0, 0, 0, 0.01, "v1", 0, 0, "", "/cwd", "", "/c/ccpulse"); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	// on_screen_32h mirrors the sibling's window exactly (pkg/cache/
	// projects_bench_test.go:65) — same 32h span — so benchstat can pair the
	// two: the fold is the only thing that should differ between them.
	b.Run("on_screen_32h", func(b *testing.B) {
		from := base
		to := base.Add(32 * time.Hour)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ModelAggregates(ctx, from, to); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full_table", func(b *testing.B) {
		to := base.Add(50_000 * time.Minute)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ModelAggregates(ctx, base, to); err != nil {
				b.Fatal(err)
			}
		}
	})
	benchModelAggregatesHighCardinality(b)
}

// benchModelAggregatesHighCardinality builds its own fixture with 200
// distinct raw model ids (none carrying a -YYYYMMDD release-date segment, so
// models.Canonical returns each unchanged — 200 distinct canonical ids too)
// and aggregates the full table. The doc comment on ModelAggregates
// (models.go:31-33) justifies folding onto models.Canonical in Go rather than
// SQL on the premise that "distinct model ids number in the low tens"; the
// sibling sub-benchmarks above only ever exercise the fold at n=7 (8 raw ids
// collapsing to 7 canonical), so that premise was never actually under test.
// This sub-benchmark pins the Go-side map fold at an order of magnitude past
// "low tens" (ccpulse-475.16).
func benchModelAggregatesHighCardinality(b *testing.B) {
	b.Helper()
	c, err := Open(context.Background(), filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	ctx := b.Context()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const distinct = 200
	mdls := make([]string, distinct)
	for i := range mdls {
		mdls[i] = fmt.Sprintf("claude-bench-model-%d", i)
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := range 50_000 {
		ts := base.Add(time.Duration(i) * time.Minute).UTC().Format(tsFormat)
		id := fmt.Sprintf("m%d", i)
		if _, err := stmt.ExecContext(ctx, id, id, "slug", ts, "assistant", mdls[i%len(mdls)],
			100, 200, 0, 0, 0, 0.01, "v1", 0, 0, "", "/cwd", "", "/c/ccpulse"); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	to := base.Add(50_000 * time.Minute)
	b.Run("distinct_200", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ModelAggregates(ctx, base, to); err != nil {
				b.Fatal(err)
			}
		}
	})
}
