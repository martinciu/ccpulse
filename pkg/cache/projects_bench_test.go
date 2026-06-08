package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkProjectAggregates measures ProjectAggregates across two scenarios:
// the realistic on-screen window (hot path) and the full-table worst case.
func BenchmarkProjectAggregates(b *testing.B) {
	c, err := Open(context.Background(), filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()

	ctx := b.Context()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	roots := []string{"/c/a", "/c/b", "/c/ccpulse", "/c/dotfiles", "/c/e", ""}
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
		root := roots[i%len(roots)]
		if _, err := stmt.ExecContext(ctx, id, id, "slug", ts, "assistant", "claude",
			100, 200, 0, 0, 0, 0.01, "v1", 0, 0, "", "/cwd", "", root); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	b.Run("full_table", func(b *testing.B) {
		from := base
		to := base.Add(50_000 * time.Minute)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ProjectAggregates(ctx, from, to); err != nil {
				b.Fatal(err)
			}
		}
	})

	// on_screen_32h approximates the visibleBuckets()-bounded window the
	// TUI actually queries on every refresh/scroll-settle — the hot path.
	b.Run("on_screen_32h", func(b *testing.B) {
		from := base
		to := base.Add(32 * time.Hour)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.ProjectAggregates(ctx, from, to); err != nil {
				b.Fatal(err)
			}
		}
	})
}
