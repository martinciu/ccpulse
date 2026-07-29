package cache

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// insertModelRow inserts one assistant message with an explicit model. Sibling
// of insertAggRow (projects_test.go), which hardcodes model="claude"; repo_root
// is a real value so the reconciliation test has both breakdowns to compare.
func insertModelRow(t *testing.T, c *Cache, model string, ts time.Time, in, out int64, cost float64) {
	t.Helper()
	id := model + ts.String()
	_, err := c.DB().ExecContext(context.Background(), `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, id, "slug", ts.UTC().Format(tsFormat), "assistant", model,
		in, out, 0, 0, 0, cost, "v1", 0, 0, "", "/cwd", "", "/code/ccpulse")
	if err != nil {
		t.Fatal(err)
	}
}

func newModelTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestModelAggregates_FoldsDatedVariants(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-haiku-4-5", base, 100, 200, 0.30)
	insertModelRow(t, c, "claude-haiku-4-5-20251001", base.Add(time.Minute), 10, 20, 0.10)

	got, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (dated variant folded), got %+v", len(got), got)
	}
	if got[0].Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", got[0].Model)
	}
	if got[0].Label != "Haiku 4.5" {
		t.Errorf("Label = %q, want Haiku 4.5", got[0].Label)
	}
	if got[0].CostUSD != 0.40 {
		t.Errorf("CostUSD = %v, want 0.40", got[0].CostUSD)
	}
	if got[0].Tokens != 330 { // (100+200) + (10+20)
		t.Errorf("Tokens = %d, want 330", got[0].Tokens)
	}
}

func TestModelAggregates_WindowFilter(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base.Add(-2*time.Hour), 1, 1, 5.00) // before
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 1.00)               // inside
	insertModelRow(t, c, "claude-opus-4-7", base.Add(2*time.Hour), 1, 1, 7.00)  // after

	got, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	if got[0].CostUSD != 1.00 {
		t.Errorf("CostUSD = %v, want 1.00 (rows outside [from,to) must be excluded)", got[0].CostUSD)
	}
}

func TestModelAggregates_SortOrderAndUnknownLast(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// The unknown bucket is given the HIGHEST cost deliberately: it must still
	// sort last.
	insertModelRow(t, c, "", base, 10, 10, 99.00)
	insertModelRow(t, c, "claude-opus-4-7", base.Add(time.Minute), 100, 200, 5.00)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(2*time.Minute), 100, 200, 1.00)

	got, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[0].Label != "Opus 4.7" {
		t.Errorf("row0 = %q, want Opus 4.7 (highest real cost first)", got[0].Label)
	}
	if got[1].Label != "Haiku 4.5" {
		t.Errorf("row1 = %q, want Haiku 4.5", got[1].Label)
	}
	if got[2].Label != "(unknown model)" {
		t.Errorf("row2 = %q, want (unknown model) forced last despite highest cost", got[2].Label)
	}
}

func TestModelAggregates_UnpricedModelRetained(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "gpt-oss:20b", base.Add(time.Minute), 1000, 2000, 0)

	got, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range got {
		if a.Label == "gpt-oss:20b" {
			found = true
			if a.Tokens != 3000 {
				t.Errorf("unpriced model Tokens = %d, want 3000", a.Tokens)
			}
			if a.CostUSD != 0 {
				t.Errorf("unpriced model CostUSD = %v, want 0", a.CostUSD)
			}
		}
	}
	if !found {
		t.Errorf("unpriced model dropped from %+v; it must render at $0 with real tokens", got)
	}
}

// TestModelAggregates_DeterministicOrder guards the third sort key: the fold
// runs through a Go map, whose iteration order is randomised per run.
func TestModelAggregates_DeterministicOrder(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Equal cost across several models — only the tiebreaks can order these.
	for i, mdl := range []string{"claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5", "claude-fable-5"} {
		insertModelRow(t, c, mdl, base.Add(time.Duration(i)*time.Minute), 10, 10, 1.00)
	}

	first, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		got, err := c.ModelAggregates(context.Background(), base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			if got[i].Model != first[i].Model {
				t.Fatalf("order not deterministic: got %+v, first run %+v", got, first)
			}
		}
	}
}

// TestModelAggregates_ReconcilesWithProjectAggregates is the contract that lets
// a user trust flipping p<->m: identical WHERE and identical SUM expressions
// mean both breakdowns must total the same for the same window.
func TestModelAggregates_ReconcilesWithProjectAggregates(t *testing.T) {
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "claude-haiku-4-5-20251001", base.Add(time.Minute), 10, 20, 0.10)
	insertModelRow(t, c, "", base.Add(2*time.Minute), 5, 5, 0.01)
	insertModelRow(t, c, "gpt-oss:20b", base.Add(3*time.Minute), 1000, 2000, 0)

	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	byModel, err := c.ModelAggregates(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	byProject, err := c.ProjectAggregates(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}

	var mCost, pCost float64
	var mTok, pTok int64
	for _, a := range byModel {
		mCost += a.CostUSD
		mTok += a.Tokens
	}
	for _, a := range byProject {
		pCost += a.CostUSD
		pTok += a.Tokens
	}
	// Cost is compared within a tolerance, not exactly: the two queries group
	// differently, so SQLite sums a different partition of the same float64
	// set and Go adds the group totals in a different order. Float addition is
	// not associative, so identical inputs legitimately differ in the last
	// bits (here 5.109999999999999 vs 5.11). Tokens are int64 — exact.
	const eps = 1e-9
	if diff := math.Abs(mCost - pCost); diff > eps {
		t.Errorf("cost totals disagree: models %v, projects %v (diff %v > %v)", mCost, pCost, diff, eps)
	}
	if mTok != pTok {
		t.Errorf("token totals disagree: models %d, projects %d", mTok, pTok)
	}
}
