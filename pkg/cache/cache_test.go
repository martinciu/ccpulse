package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func TestOpenAppliesSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	row := c.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('messages','files','slug_canonical','meta')`)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 tables, got %d", n)
	}
}

func TestInsertMessages(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	msgs := []parse.Message{
		{
			SessionID:   "s1",
			ProjectSlug: "slug-a",
			Model:       "claude-opus-4-7",
			Timestamp:   time.Now(),
			InputTokens: 10,
		},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count = %d, want 1", n)
	}

	var cost float64
	var unknown int
	if err := c.DB().QueryRow(`SELECT cost_usd_estimate, pricing_unknown FROM messages`).Scan(&cost, &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 0 {
		t.Errorf("pricing_unknown = %d, want 0", unknown)
	}
	if cost <= 0 {
		t.Errorf("cost = %v, want > 0", cost)
	}
}

func TestFileTracking(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.RecordFile("/tmp/x.jsonl", 1234, 5678, 42); err != nil {
		t.Fatal(err)
	}
	mtime, off, line, found, err := c.GetFile("/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mtime != 1234 || off != 5678 || line != 42 {
		t.Errorf("got mtime=%d off=%d line=%d found=%v", mtime, off, line, found)
	}

	// Update existing record
	if err := c.RecordFile("/tmp/x.jsonl", 9999, 8888, 99); err != nil {
		t.Fatal(err)
	}
	mtime, _, _, _, _ = c.GetFile("/tmp/x.jsonl")
	if mtime != 9999 {
		t.Errorf("after update mtime = %d", mtime)
	}
}

func TestInsertMessagesIdempotent(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Now()
	msgs := []parse.Message{
		{
			SessionID:   "s1",
			ProjectSlug: "slug-a",
			Model:       "claude-opus-4-7",
			Timestamp:   ts,
			InputTokens: 10,
		},
	}

	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count after duplicate insert = %d, want 1", n)
	}
}
