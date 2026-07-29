package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkModelAggregates mirrors BenchmarkProjectAggregates: the realistic
// on-screen window (hot path) and the full-table worst case. Its purpose is to
// confirm the Go-side canonical fold does not turn a cheap query into an
// allocation story.
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

	b.Run("visible-window", func(b *testing.B) {
		from := base.Add(1000 * time.Minute)
		to := from.Add(120 * time.Minute)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ModelAggregates(ctx, from, to); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full-table", func(b *testing.B) {
		to := base.Add(50_000 * time.Minute)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ModelAggregates(ctx, base, to); err != nil {
				b.Fatal(err)
			}
		}
	})
}
