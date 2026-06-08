package cache

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSchema_HasRepoRoot(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var n int
	if err := c.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='repo_root'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages.repo_root column count = %d, want 1", n)
	}

	if SchemaVersion != "8" {
		t.Fatalf("SchemaVersion = %q, want 8", SchemaVersion)
	}
}

func TestProjectAggregates_RollupAndNoProject(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Two rows for the same repo_root (main + worktree fold to one root)
	// and one row with empty repo_root (no project).
	insertAggRow(t, c, "/code/ccpulse", base, 100, 200, 1.50)
	insertAggRow(t, c, "/code/ccpulse", base.Add(time.Minute), 10, 20, 0.50)
	insertAggRow(t, c, "", base.Add(2*time.Minute), 5, 5, 0.10)

	got, err := c.ProjectAggregates(context.Background(),
		base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (ccpulse + no-project)", len(got))
	}
	// Sorted cost desc → ccpulse first, (no project) forced last.
	if got[0].Label != "ccpulse" || got[0].RepoRoot != "/code/ccpulse" {
		t.Errorf("row0 = %+v, want ccpulse", got[0])
	}
	if got[0].CostUSD != 2.00 {
		t.Errorf("ccpulse cost = %v, want 2.00", got[0].CostUSD)
	}
	if got[0].Tokens != 330 { // (100+200) + (10+20)
		t.Errorf("ccpulse tokens = %d, want 330", got[0].Tokens)
	}
	if got[1].Label != "(no project)" {
		t.Errorf("last row = %q, want (no project)", got[1].Label)
	}
	// %total is share of window cost: 2.00 / (2.00 + 0.10) ≈ 95.2%.
	if got[0].CostPct < 94 || got[0].CostPct > 96 {
		t.Errorf("ccpulse CostPct = %v, want ~95.2", got[0].CostPct)
	}
}

func TestProjectAggregates_WindowFilter(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertAggRow(t, c, "/code/in", base, 1, 1, 1.0)
	insertAggRow(t, c, "/code/out", base.Add(-2*time.Hour), 1, 1, 9.0)

	got, err := c.ProjectAggregates(context.Background(),
		base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "in" {
		t.Fatalf("got %+v, want only /code/in", got)
	}
}

// insertAggRow writes one minimal messages row with the given repo_root,
// timestamp, token split, and cost. Direct SQL keeps the test independent
// of the ingest path.
func insertAggRow(t *testing.T, c *Cache, repoRoot string, ts time.Time, in, out int64, cost float64) {
	t.Helper()
	_, err := c.DB().ExecContext(context.Background(), `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		repoRoot+ts.String(), repoRoot+ts.String(), "slug",
		ts.UTC().Format("2006-01-02T15:04:05Z"), "assistant", "claude",
		in, out, 0, 0, 0, cost, "v1", 0, 0, "", "/cwd", "", repoRoot)
	if err != nil {
		t.Fatal(err)
	}
}
