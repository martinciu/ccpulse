package cache_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func mustOpenTempCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "s.db"))
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
	if err := c.InsertMessages([]parse.Message{m}, hist); err != nil {
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

	first, err := c.Recost(context.Background(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("first recost: %v", err)
	}
	if first.Updated != 0 {
		t.Errorf("first recost on freshly-inserted row updated %d rows, want 0", first.Updated)
	}
	second, err := c.Recost(context.Background(), hist, cache.RecostOpts{})
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

	stats, err := c.Recost(context.Background(), hist, cache.RecostOpts{})
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

func TestRecost_ClearsUnknownWhenModelAdded(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	// Insert claude-haiku-4-5 at 2026-05-09 ts — model missing from 2026-05-09 table.
	m := parse.Message{
		SessionID: "s1", ProjectSlug: "p", Role: "assistant",
		Model: "claude-haiku-4-5", InputTokens: 1_000_000,
		Timestamp: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	seedRow(t, c, hist, m)
	var unk int
	if err := c.DB().QueryRow(`SELECT pricing_unknown FROM messages WHERE session_id = ?`, m.SessionID).Scan(&unk); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if unk != 1 {
		t.Fatalf("seed pricing_unknown = %d, want 1", unk)
	}

	// Move the row's ts to 2026-05-10 so haiku is now known.
	if _, err := c.DB().Exec(`UPDATE messages SET ts = '2026-05-10T00:00:00.000Z' WHERE session_id = ?`, m.SessionID); err != nil {
		t.Fatalf("update ts: %v", err)
	}
	stats, err := c.Recost(context.Background(), hist, cache.RecostOpts{})
	if err != nil {
		t.Fatalf("recost: %v", err)
	}
	if stats.Updated != 1 {
		t.Errorf("Updated = %d, want 1", stats.Updated)
	}
	if err := c.DB().QueryRow(`SELECT pricing_unknown FROM messages WHERE session_id = ?`, m.SessionID).Scan(&unk); err != nil {
		t.Fatalf("re-read row: %v", err)
	}
	if unk != 0 {
		t.Errorf("pricing_unknown after recost = %d, want 0", unk)
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
	stats, err := c.Recost(context.Background(), hist, cache.RecostOpts{})
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
	stats, err := c.Recost(context.Background(), hist, cache.RecostOpts{DryRun: true})
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

func TestRecost_ContextCancellation(t *testing.T) {
	c := mustOpenTempCache(t)
	hist := twoVersionHistory(t)
	for i := 0; i < 10; i++ {
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
	_, err := c.Recost(ctx, hist, cache.RecostOpts{})
	if err == nil {
		t.Errorf("recost with cancelled ctx returned nil error, want non-nil")
	}
	var stale int
	if err := c.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE pricing_version = '2026-05-10'`).Scan(&stale); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stale != 10 {
		t.Errorf("cancelled recost wrote rows: %d still stale (want 10 — rollback)", stale)
	}
}
