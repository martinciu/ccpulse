package cache_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func mustOpenTempCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// twoVersionHistory returns a History with deterministic 2026-05-09 (input=15)
// and 2026-05-10 (input=5) entries for one model. Keeps recost tests free
// from embed coupling.
func twoVersionHistory(t *testing.T) pricing.History {
	t.Helper()
	old := pricing.Table{
		Version:  "2026-05-09",
		Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-7": {InputPerMtok: 15, OutputPerMtok: 75},
		},
	}
	cur := pricing.Table{
		Version:  "2026-05-10",
		Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-7":  {InputPerMtok: 5, OutputPerMtok: 25},
			"claude-haiku-4-5": {InputPerMtok: 1, OutputPerMtok: 5},
		},
	}
	h, err := pricing.HistoryForTest([]pricing.Table{old, cur})
	if err != nil {
		t.Fatalf("HistoryForTest: %v", err)
	}
	return h
}

func seedRow(t *testing.T, c *cache.Cache, hist pricing.History, m parse.Message) {
	t.Helper()
	if err := c.InsertMessages(t.Context(), []parse.Message{m}, hist); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
}

func TestRecost_Idempotent(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	seedRow(t, c, hist, parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
	})

	first, err := c.Recost(t.Context(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("first recost: %v", err)
	}
	if first.Updated != 0 {
		t.Errorf("first recost on freshly-inserted row updated %d rows, want 0", first.Updated)
	}
	second, err := c.Recost(t.Context(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("second recost: %v", err)
	}
	if second.Updated != 0 {
		t.Errorf("second recost updated %d rows, want 0", second.Updated)
	}
}

func TestRecost_FixesStaleVersion(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	// Simulate a stale stamp written before history existed.
	if _, err := c.DB().Exec(`UPDATE messages SET pricing_version = '2026-05-10', cost_usd_estimate = 5.0 WHERE session_id = ?`, m.SessionID); err != nil {
		t.Fatalf("seed stale version: %v", err)
	}

	stats, err := c.Recost(t.Context(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("recost: %v", err)
	}
	if stats.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", stats.Updated)
	}
	var ver string
	var cost float64
	if err := c.DB().QueryRow(`SELECT pricing_version, cost_usd_estimate FROM messages WHERE session_id = ?`, m.SessionID).Scan(&ver, &cost); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if ver != "2026-05-09" {
		t.Errorf("pricing_version = %q, want 2026-05-09", ver)
	}
	if cost <= 0 || cost > 20 {
		t.Errorf("cost_usd_estimate = %v, want ~15 (input=15, 1M tokens)", cost)
	}
}

func TestRecost_FallForwardRescuesOrphanedRow(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t) // claude-haiku-4-5 exists only in the 2026-05-10 table
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-haiku-4-5", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	// Simulate a pre-fall-forward orphaned stamp: the 2026-05-09 snapshot lacked
	// haiku, so a pre-#368 build costed the row 0 / unknown=1 / date-resolved
	// version. Recost must now rescue it via fall-forward.
	if _, err := c.DB().Exec(
		`UPDATE messages SET pricing_unknown = 1, cost_usd_estimate = 0, pricing_version = '2026-05-09' WHERE session_id = ?`,
		m.SessionID); err != nil {
		t.Fatalf("seed orphaned state: %v", err)
	}

	stats, err := c.Recost(t.Context(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("recost: %v", err)
	}
	if stats.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", stats.Updated)
	}

	var unk int
	var cost float64
	var ver string
	if err := c.DB().QueryRow(
		`SELECT pricing_unknown, cost_usd_estimate, pricing_version FROM messages WHERE session_id = ?`,
		m.SessionID).Scan(&unk, &cost, &ver); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if unk != 0 {
		t.Errorf("pricing_unknown = %d, want 0 (rescued)", unk)
	}
	if cost != 1.0 { // haiku InputPerMtok=1 * 1Mtok = 1.0
		t.Errorf("cost_usd_estimate = %v, want 1.0", cost)
	}
	if ver != "2026-05-10" {
		t.Errorf("pricing_version = %q, want 2026-05-10 (fall-forward rate source)", ver)
	}
}

func TestRecost_PreservesUnknownWhenModelMissing(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "no-such-model", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	stats, err := c.Recost(t.Context(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("recost: %v", err)
	}
	if stats.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (unknown -> unknown is a no-op)", stats.Updated)
	}
}

func TestRecost_DryRunNoWrites(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	if _, err := c.DB().Exec(`UPDATE messages SET pricing_version = '2026-05-10' WHERE session_id = ?`, m.SessionID); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	stats, err := c.Recost(t.Context(), hist, cache.RecostOpts{DryRun: true})
	if err != nil {
		t.Fatalf("recost dry-run: %v", err)
	}
	if stats.Updated != 1 {
		t.Errorf("dry-run Updated count = %d, want 1 (planned)", stats.Updated)
	}
	var ver string
	if err := c.DB().QueryRow(`SELECT pricing_version FROM messages WHERE session_id = ?`, m.SessionID).Scan(&ver); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if ver != "2026-05-10" {
		t.Errorf("pricing_version after dry-run = %q, want 2026-05-10 (unchanged)", ver)
	}
}

func TestPricingVersionStats(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	seedRow(t, c, hist, parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	})
	seedRow(t, c, hist, parse.Message{
		SessionID: "s2", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
	})
	// Stamp one row with a stale version.
	if _, err := c.DB().Exec(`UPDATE messages SET pricing_version = '1999-01-01' WHERE session_id = 's1'`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	got, err := c.PricingVersionStats(t.Context(), hist)
	if err != nil {
		t.Fatalf("PricingVersionStats: %v", err)
	}
	var seenStale, seenCurrent bool
	for _, s := range got {
		switch s.Version {
		case "1999-01-01":
			seenStale = true
			if s.Rows != 1 || s.Stale != 1 || s.IsCurrent {
				t.Errorf("stale entry = %+v", s)
			}
		case "2026-05-10":
			seenCurrent = true
			if s.Rows != 1 || s.Stale != 0 || !s.IsCurrent {
				t.Errorf("current entry = %+v", s)
			}
		}
	}
	if !seenStale || !seenCurrent {
		t.Errorf("missing expected entries; got %+v", got)
	}
}

func TestPricingVersionStats_FallForwardNotStale(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	// haiku@2026-05-09 falls forward to 2026-05-10 at ingest, so it is stamped
	// 2026-05-10 even though TableAt(2026-05-09) == 2026-05-09.
	seedRow(t, c, hist, parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-haiku-4-5", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	})

	got, err := c.PricingVersionStats(t.Context(), hist)
	if err != nil {
		t.Fatalf("PricingVersionStats: %v", err)
	}
	var found bool
	for _, s := range got {
		if s.Version == "2026-05-10" {
			found = true
			if s.Stale != 0 {
				t.Errorf("fall-forward row counted stale: %+v (TableAt(2026-05-09)=2026-05-09 but VersionFor=2026-05-10)", s)
			}
		}
	}
	if !found {
		t.Fatalf("no 2026-05-10 entry; got %+v", got)
	}
}

func TestRecost_ContextCancellation(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	for i := range 10 {
		seedRow(t, c, hist, parse.Message{
			SessionID: "s" + string(rune('a'+i)), ProjectSlug: "p", Role: "assistant",
			Model: "claude-opus-4-7", InputTokens: 1_000_000,
			Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		})
	}
	if _, err := c.DB().Exec(`UPDATE messages SET pricing_version = '2026-05-10'`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := c.Recost(ctx, hist, cache.RecostOpts{})
	if err == nil {
		t.Errorf("recost with cancelled ctx returned nil error, want non-nil")
	}
	if stats.Queued < 0 {
		t.Errorf("stats.Queued = %d, want >= 0", stats.Queued)
	}
	var stale int
	if err := c.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE pricing_version = '2026-05-10'`).Scan(&stale); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stale != 10 {
		t.Errorf("cancelled recost wrote rows: %d still stale (want 10 — rollback)", stale)
	}
}

func TestAutoRecost_SkipsWhenFingerprintMatches(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	// Stamp the row with a stale pricing_version so Recost would normally update it.
	if _, err := c.DB().Exec(`UPDATE messages SET pricing_version = '1999-01-01' WHERE session_id = ?`, m.SessionID); err != nil {
		t.Fatalf("seed stale version: %v", err)
	}
	// Pre-write the matching fingerprint into meta so AutoRecost short-circuits.
	fp := "ff1:" + strings.Join(hist.Versions(), ",")
	if _, err := c.DB().Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('last_recost_history_fingerprint',?)`, fp); err != nil {
		t.Fatalf("seed fingerprint: %v", err)
	}

	c.AutoRecost(t.Context(), hist)

	// Row must still be stale — the early-out prevented any rewrite.
	var ver string
	if err := c.DB().QueryRow(`SELECT pricing_version FROM messages WHERE session_id = ?`, m.SessionID).Scan(&ver); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if ver != "1999-01-01" {
		t.Errorf("pricing_version = %q after AutoRecost with matching fingerprint, want 1999-01-01 (skipped)", ver)
	}
}

func TestAutoRecost_RunsAfterAlgorithmBump(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-haiku-4-5", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	// Orphaned pre-#368 state + an OLD-FORMAT fingerprint (version list, no algo
	// tag) as a pre-#368 build would have written it.
	if _, err := c.DB().Exec(
		`UPDATE messages SET pricing_unknown = 1, cost_usd_estimate = 0, pricing_version = '2026-05-09' WHERE session_id = ?`,
		m.SessionID); err != nil {
		t.Fatalf("seed orphaned state: %v", err)
	}
	oldFP := strings.Join(hist.Versions(), ",") // no "ff1:" prefix
	if _, err := c.DB().Exec(
		`INSERT OR REPLACE INTO meta(key,value) VALUES('last_recost_history_fingerprint',?)`, oldFP); err != nil {
		t.Fatalf("seed old fingerprint: %v", err)
	}

	c.AutoRecost(t.Context(), hist) // new tag != stored ⇒ must NOT short-circuit

	var unk int
	if err := c.DB().QueryRow(`SELECT pricing_unknown FROM messages WHERE session_id = ?`, m.SessionID).Scan(&unk); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if unk != 0 {
		t.Errorf("pricing_unknown = %d after AutoRecost, want 0 (algorithm bump should trigger rescue)", unk)
	}
	var fp string
	if err := c.DB().QueryRow(`SELECT value FROM meta WHERE key = 'last_recost_history_fingerprint'`).Scan(&fp); err != nil {
		t.Fatalf("read fingerprint: %v", err)
	}
	if want := "ff1:" + strings.Join(hist.Versions(), ","); fp != want {
		t.Errorf("fingerprint = %q, want %q", fp, want)
	}
}

func TestRecost_WritesFingerprintOnCommit(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	seedRow(t, c, hist, parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	})

	// Non-dry-run: fingerprint must be written.
	if _, err := c.Recost(t.Context(), hist, cache.RecostOpts{}); err != nil {
		t.Fatalf("recost: %v", err)
	}
	var got string
	if err := c.DB().QueryRow(`SELECT value FROM meta WHERE key = 'last_recost_history_fingerprint'`).Scan(&got); err != nil {
		t.Fatalf("read fingerprint: %v", err)
	}
	want := "ff1:" + strings.Join(hist.Versions(), ",")
	if got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}

	// Dry-run on a second cache: fingerprint must NOT be written.
	c2 := mustOpenTempCache(t)
	seedRow(t, c2, hist, parse.Message{
		SessionID: "s2", ProjectSlug: "p", Role: "assistant",
		Model: "claude-opus-4-7", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	})
	// Force a stale stamp so dry-run has work to plan.
	if _, err := c2.DB().Exec(`UPDATE messages SET pricing_version = '1999-01-01'`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if _, err := c2.Recost(t.Context(), hist, cache.RecostOpts{DryRun: true}); err != nil {
		t.Fatalf("recost dry-run: %v", err)
	}
	var dryGot string
	err := c2.DB().QueryRow(`SELECT value FROM meta WHERE key = 'last_recost_history_fingerprint'`).Scan(&dryGot)
	if err == nil {
		t.Errorf("dry-run wrote fingerprint %q, want no row", dryGot)
	}
}
