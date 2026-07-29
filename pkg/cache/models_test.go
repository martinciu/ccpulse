package cache

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// insertModelRow inserts one assistant message with an explicit model. Sibling
// of insertAggRow (projects_test.go), which hardcodes model="claude"; repo_root
// is a real value so the reconciliation test has both breakdowns to compare.
//
// cache_read_tokens/cache_write_5m_tokens/cache_write_1h_tokens are fixed
// non-zero values (not zero, and not folded into in/out) so a SUM-expression
// drift in ModelAggregates — e.g. someone later adding a cache column to the
// query — is caught by TestModelAggregates_ReconcilesWithProjectAggregates
// instead of passing silently.
func insertModelRow(t *testing.T, c *Cache, model string, ts time.Time, in, out int64, cost float64) {
	t.Helper()
	const cacheRead, cacheWrite5m, cacheWrite1h = 700, 70, 7
	id := model + ts.String()
	_, err := c.DB().ExecContext(t.Context(), `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, id, "slug", ts.UTC().Format(tsFormat), "assistant", model,
		in, out, cacheRead, cacheWrite5m, cacheWrite1h, cost, "v1", 0, 0, "", "/cwd", "", "/code/ccpulse")
	if err != nil {
		t.Fatal(err)
	}
}

func newModelTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestModelAggregates_FoldsDatedVariants(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-haiku-4-5", base, 100, 200, 0.30)
	insertModelRow(t, c, "claude-haiku-4-5-20251001", base.Add(time.Minute), 10, 20, 0.10)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
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
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	insertModelRow(t, c, "claude-opus-4-7", base.Add(-2*time.Hour), 1, 1, 5.00) // before
	insertModelRow(t, c, "claude-opus-4-7", from, 1, 1, 3.00)                   // exactly `from` (INCLUDED)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 1.00)               // inside
	insertModelRow(t, c, "claude-opus-4-7", to, 1, 1, 4.00)                     // exactly `to` (EXCLUDED)
	insertModelRow(t, c, "claude-opus-4-7", base.Add(2*time.Hour), 1, 1, 7.00)  // after

	got, err := c.ModelAggregates(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	// 1.00 (inside) + 3.00 (exactly `from`, included) = 4.00. The row at
	// exactly `to` (4.00) and the strictly-outside rows (5.00, 7.00) must all
	// be excluded — a half-open boundary flip (`> from` or `<= to`) would
	// move the total to 5.00 or 8.00 respectively.
	if got[0].CostUSD != 4.00 {
		t.Errorf("CostUSD = %v, want 4.00 ([from,to) boundary: from included, to excluded)", got[0].CostUSD)
	}
}

func TestModelAggregates_SortOrderAndUnknownLast(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// The unknown bucket is given the HIGHEST cost deliberately: it must still
	// sort last.
	insertModelRow(t, c, "", base, 10, 10, 99.00)
	insertModelRow(t, c, "claude-opus-4-7", base.Add(time.Minute), 100, 200, 5.00)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(2*time.Minute), 100, 200, 1.00)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
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
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "gpt-oss:20b", base.Add(time.Minute), 1000, 2000, 0)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
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

// TestModelAggregates_CostPct asserts the %-of-window-cost column, mirroring
// TestProjectAggregates_RollupAndNoProject (projects_test.go:67-70). The
// dated-variant fold is the case worth guarding here: two dated variants of
// the same canonical model must fold into ONE row before the percentage is
// computed, so the row reports their COMBINED share of the window cost — a
// share TestProjectAggregates_RollupAndNoProject cannot exercise since
// ProjectAggregates has no equivalent fold.
func TestModelAggregates_CostPct(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 2.00)
	insertModelRow(t, c, "claude-opus-4-7-20251001", base.Add(time.Minute), 10, 20, 1.00)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(2*time.Minute), 50, 50, 1.00)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (opus variants folded + haiku)", len(got))
	}
	if got[0].Model != "claude-opus-4-7" || got[0].CostUSD != 3.00 {
		t.Fatalf("row0 = %+v, want claude-opus-4-7 at 3.00 (2.00+1.00 combined)", got[0])
	}
	// %total is share of window cost: 3.00 / (3.00 + 1.00) = 75%.
	if got[0].CostPct < 74 || got[0].CostPct > 76 {
		t.Errorf("opus CostPct = %v, want ~75 (combined dated-variant share)", got[0].CostPct)
	}
	if got[1].Model != "claude-haiku-4-5" || got[1].CostUSD != 1.00 {
		t.Fatalf("row1 = %+v, want claude-haiku-4-5 at 1.00", got[1])
	}
	if got[1].CostPct < 24 || got[1].CostPct > 26 {
		t.Errorf("haiku CostPct = %v, want ~25", got[1].CostPct)
	}
}

// TestModelAggregates_DeterministicOrder guards the Label sort key: the fold
// runs through a Go map, whose iteration order is randomised per run, so
// equal-cost rows need a total order or the box would flicker across
// refreshes.
//
// The fixture's four labels (Fable 5, Opus 4.7, Opus 4.8, Sonnet 5) are all
// distinct, so this cannot reach the third key (Model). Under
// pkg/models.Label, two DIFFERENT canonical ids cannot produce an identical
// label for any realistic "claude-<family>-<numeric...>" id — the label is
// reconstructible back to the canonical id (family + numeric segments), so
// equal Label implies equal Model. The only way to force a collision would
// be a canonical id that is literally not modern Claude-shaped but happens
// to render verbatim as an existing label string (e.g. a raw id "Opus 4.7"),
// which is not a realistic transcript model id — that fixture was not added
// here as it would be testing a fabricated case rather than the real one.
func TestModelAggregates_DeterministicOrder(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Equal cost across several models — only the Label tiebreak can order these.
	for i, mdl := range []string{"claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5", "claude-fable-5"} {
		insertModelRow(t, c, mdl, base.Add(time.Duration(i)*time.Minute), 10, 10, 1.00)
	}
	wantOrder := []string{"claude-fable-5", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5"}

	first, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(wantOrder) {
		t.Fatalf("rows = %d, want %d, got %+v", len(first), len(wantOrder), first)
	}
	for i, want := range wantOrder {
		if first[i].Model != want {
			t.Fatalf("order = %+v, want %v (Label tiebreak: Fable 5 < Opus 4.7 < Opus 4.8 < Sonnet 5)", first, wantOrder)
		}
	}

	for range 20 {
		got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(first) {
			t.Fatalf("row count not deterministic: got %d rows, first run had %d: got %+v, first %+v", len(got), len(first), got, first)
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
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "claude-haiku-4-5-20251001", base.Add(time.Minute), 10, 20, 0.10)
	insertModelRow(t, c, "", base.Add(2*time.Minute), 5, 5, 0.01)
	insertModelRow(t, c, "gpt-oss:20b", base.Add(3*time.Minute), 1000, 2000, 0)

	from, to := base.Add(-time.Hour), base.Add(time.Hour)
	byModel, err := c.ModelAggregates(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	byProject, err := c.ProjectAggregates(t.Context(), from, to)
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
