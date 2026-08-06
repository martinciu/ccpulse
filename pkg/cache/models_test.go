package cache

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
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

// TestModelAggregates_UnpricedModelRetained also guards against
// over-filtering by the #484 zero-contribution drop: gpt-oss:20b has zero
// cost but non-zero tokens, so it fails the "zero tokens AND zero cost"
// condition and must survive the filter — unlike a true zero-contribution
// row (TestModelAggregates_DropsZeroContributionRow), which is zero on both.
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

// TestModelAggregates_CostPctClamped guards the max(0, min(100, ...)) clamp at
// models.go:93. Two rows whose costs nearly cancel drive the window total
// toward zero while an individual model's CostUSD stays large, so the
// unclamped ratio a.CostUSD/total*100 blows out to roughly +/-1e15 (see the
// clamp comment at models.go:86-92: token counts are never validated on
// ingest, so a mixed-sign cost_usd_estimate can produce exactly this shape).
// Reverting the clamp to the plain division must fail this test.
func TestModelAggregates_CostPctClamped(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 1000)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(time.Minute), 50, 50, -999.9999999999)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	for _, a := range got {
		if a.CostPct < 0 || a.CostPct > 100 {
			t.Errorf("CostPct for %q = %v, want within [0, 100] (near-zero window total must not blow the ratio out of range)", a.Model, a.CostPct)
		}
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

// TestModelAggregates_DropsZeroContributionRow guards issue #484: a canonical
// row that contributes zero tokens AND zero cost (Claude Code's own
// "<synthetic>" marker for locally-produced assistant turns is the real-world
// case, but the rule is purely "contributes nothing" — no id is hardcoded)
// must be absent from the result, while a real model in the same window is
// still present.
func TestModelAggregates_DropsZeroContributionRow(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "<synthetic>", base.Add(time.Minute), 0, 0, 0)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (zero-contribution row dropped), got %+v", len(got), got)
	}
	if got[0].Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", got[0].Model)
	}
}

// TestModelAggregates_OnlyZeroContributionRowsYieldsEmpty asserts that a
// window containing only zero-contribution rows yields an empty result,
// which the TUI's renderBreakdownBox then consumes to display a placeholder
// when no models contributed to the window.
func TestModelAggregates_OnlyZeroContributionRowsYieldsEmpty(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "<synthetic>", base, 0, 0, 0)
	insertModelRow(t, c, "<synthetic>", base.Add(time.Minute), 0, 0, 0)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %d, want 0 (only zero-contribution rows in window), got %+v", len(got), got)
	}
}

// TestModelAggregates_CacheOnlyTurnRetained guards the OTHER half of the
// zero-contribution filter's conjunction (a.CostUSD == 0 && a.Tokens == 0):
// symmetric to TestModelAggregates_UnpricedModelRetained, which covers
// tokens>0/cost==0, this covers tokens==0/cost>0. ModelAggregate.Tokens is
// SUM(input_tokens + output_tokens) only, while cost_usd_estimate also prices
// cache_read_tokens / cache_write_5m_tokens / cache_write_1h_tokens
// (pkg/pricing/pricing.go:187-191), so a cache-read-dominated turn can have
// zero I/O tokens and still carry real cost. Weakening the filter's condition
// from `a.CostUSD == 0 && a.Tokens == 0` to `a.Tokens == 0` alone would drop
// this row — this fixture is the one that catches it, since no other fixture
// in this file has Tokens==0 with CostUSD!=0.
func TestModelAggregates_CacheOnlyTurnRetained(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 100, 200, 5.00)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(time.Minute), 0, 0, 2.50)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range got {
		if a.Label == "Haiku 4.5" {
			found = true
			if a.Tokens != 0 {
				t.Errorf("cache-only turn Tokens = %d, want 0", a.Tokens)
			}
			if a.CostUSD != 2.50 {
				t.Errorf("cache-only turn CostUSD = %v, want 2.50", a.CostUSD)
			}
		}
	}
	if !found {
		t.Errorf("cache-only turn (tokens=0, cost>0) dropped from %+v; it must render at its real cost with 0 I/O tokens", got)
	}
}

// TestModelAggregates_DropsCancelledActivity pins the deliberate consequence
// of the zero-contribution filter running post-fold rather than pre-fold: a
// model with real activity on BOTH sides vanishes entirely if its folded sums
// cancel to exactly (0, 0). Nothing validates token/cost signs on ingest, so
// this shape is reachable from real (if malformed) transcript data, and
// IEEE-754 makes exact negations sum to exactly 0.0 rather than some
// near-zero residue.
//
// This is a recorded decision (see the comment at models.go:107-121), not an
// accident of where the filter sits: such a row contributes zero to every
// panel total whether shown or not, and mixed-sign token counts are already
// malformed input — the same premise TestModelAggregates_CostPctClamped's
// clamp rests on.
//
// The two rows deliberately use DIFFERENT raw ids (a base id and its dated
// variant) that fold to the SAME canonical model, rather than one raw id
// twice: `GROUP BY model` in the SQL already sums same-raw-id rows before any
// Go code runs, so a same-raw-id fixture cancels before reaching either a
// pre-fold or a post-fold filter and cannot tell the two placements apart.
// With distinct raw ids, each SQL-grouped row is individually non-zero
// (5.00/1500 and -5.00/-1500) and only cancels once Go's canonical fold
// merges them — so this fixture fails if the filter is moved into the scan
// loop (filtering each raw SQL row before the fold), proving placement still
// matters for this one case even though it doesn't for the "zero-token dated
// variant" case the corrected models.go comment addresses.
func TestModelAggregates_DropsCancelledActivity(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	insertModelRow(t, c, "claude-opus-4-7", base, 1000, 500, 5.00)
	insertModelRow(t, c, "claude-opus-4-7-20251001", base.Add(time.Minute), -1000, -500, -5.00)
	insertModelRow(t, c, "claude-haiku-4-5", base.Add(2*time.Minute), 10, 10, 1.00)

	got, err := c.ModelAggregates(t.Context(), base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (cancelled opus row dropped, haiku retained), got %+v", len(got), got)
	}
	if got[0].Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", got[0].Model)
	}
	for _, a := range got {
		if a.Model == "claude-opus-4-7" {
			t.Errorf("cancelled-activity row %+v present in result, want dropped", a)
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

// TestModelAggregates_AttemptRowsRedistribute pins issue #456: a fallback
// turn parsed into parent + attempt rows lands as two messages rows whose
// tokens and cost group under their own models, and re-inserting the same
// parse output is a no-op (idempotent upsert on identical input — not a
// stand-in for real multi-line streaming, which is covered separately by
// TestModelAggregates_MixedPresenceLinesNoDoubleCount).
func TestModelAggregates_AttemptRowsRedistribute(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)

	line := `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"iterations":[{"input_tokens":2,"output_tokens":434,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-fable-5"},{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]}}}` + "\n"

	msgs, err := parse.Parse(strings.NewReader(line), "slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("parsed %d messages, want 2 (parent + attempt)", len(msgs))
	}

	hist, err := pricing.HistoryForTest([]pricing.Table{{
		Version: "2026-01-01", Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-8": {InputPerMtok: 10, OutputPerMtok: 100},
			"claude-fable-5":  {InputPerMtok: 20, OutputPerMtok: 200},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 { // second insert must be a no-op (idempotent upsert)
		if err := c.InsertMessages(t.Context(), msgs, hist); err != nil {
			t.Fatal(err)
		}
	}

	from := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	got, err := c.ModelAggregates(t.Context(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("aggregates = %d rows, want 2 (opus + fable): %+v", len(got), got)
	}
	byModel := map[string]ModelAggregate{}
	for _, a := range got {
		byModel[a.Model] = a
	}
	opus, fable := byModel["claude-opus-4-8"], byModel["claude-fable-5"]
	if opus.Tokens != 2301 { // 2 + 2299
		t.Errorf("opus tokens = %d, want 2301", opus.Tokens)
	}
	if fable.Tokens != 436 { // 2 + 434 — the refused attempt, invisible before #456
		t.Errorf("fable tokens = %d, want 436", fable.Tokens)
	}
	const M = 1_000_000.0
	wantOpus := 2.0/M*10 + 2299.0/M*100
	wantFable := 2.0/M*20 + 434.0/M*200
	if diff := opus.CostUSD - wantOpus; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("opus cost = %v, want %v", opus.CostUSD, wantOpus)
	}
	if diff := fable.CostUSD - wantFable; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("fable cost = %v, want %v", fable.CostUSD, wantFable)
	}
}

// TestModelAggregates_MixedPresenceLinesNoDoubleCount pins issue #456's real
// multi-line shape: Claude Code turns stream across several JSONL lines that
// share one message.id, and usage.iterations only appears on the FINAL line
// of that turn. Earlier lines carry PARTIAL outer usage and no
// usage.iterations key at all — lower own-model tokens than the final line.
// Because InsertMessages upserts MAX per token column, the partial line's
// lower own-sums are superseded once the final (iterations-bearing) line
// lands, so nothing double-counts and the partial line leaves no stray row
// behind.
func TestModelAggregates_MixedPresenceLinesNoDoubleCount(t *testing.T) {
	t.Parallel()
	c := newModelTestCache(t)

	// line1: an early streamed line for message m1 — no usage.iterations key,
	// partial own-model usage (out=100, well below the final line's out=2299).
	line1 := `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}` + "\n"
	// line2: the final line for the same message — full own-model usage plus
	// the iterations array (foreign claude-fable-5 out=434, own fallback
	// claude-opus-4-8 out=2299).
	line2 := `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-21T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"iterations":[{"input_tokens":2,"output_tokens":434,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message","model":"claude-fable-5"},{"input_tokens":2,"output_tokens":2299,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"fallback_message","model":"claude-opus-4-8"}]}}}` + "\n"

	msgs1, err := parse.Parse(strings.NewReader(line1), "slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs1) != 1 {
		t.Fatalf("parsed line1 = %d messages, want 1 (no iterations key on the partial line)", len(msgs1))
	}

	msgs2, err := parse.Parse(strings.NewReader(line2), "slug")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 2 {
		t.Fatalf("parsed line2 = %d messages, want 2 (parent + attempt)", len(msgs2))
	}

	hist, err := pricing.HistoryForTest([]pricing.Table{{
		Version: "2026-01-01", Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-8": {InputPerMtok: 10, OutputPerMtok: 100},
			"claude-fable-5":  {InputPerMtok: 20, OutputPerMtok: 200},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Insert the two lines separately and in order, as the watcher would tail
	// them from the file: the absent-iterations partial line first, the
	// iterations-bearing final line second.
	if err := c.InsertMessages(t.Context(), msgs1, hist); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertMessages(t.Context(), msgs2, hist); err != nil {
		t.Fatal(err)
	}

	var rowCount int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 2 {
		t.Fatalf("messages row count = %d, want 2 (parent + attempt only — the partial line must not leave a stray row)", rowCount)
	}

	from := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	got, err := c.ModelAggregates(t.Context(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("aggregates = %d rows, want 2 (opus + fable): %+v", len(got), got)
	}
	byModel := map[string]ModelAggregate{}
	for _, a := range got {
		byModel[a.Model] = a
	}
	opus, fable := byModel["claude-opus-4-8"], byModel["claude-fable-5"]
	if opus.Tokens != 2301 { // 2 + 2299 — MAX upsert lands on line2's own-sums, not 2+100 and not a doubled total
		t.Errorf("opus tokens = %d, want 2301", opus.Tokens)
	}
	if fable.Tokens != 436 { // 2 + 434 — only ever inserted once, by line2
		t.Errorf("fable tokens = %d, want 436", fable.Tokens)
	}
}
