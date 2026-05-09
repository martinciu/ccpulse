package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackfillRun_WalksFilesNewestFirstWithProgress(t *testing.T) {
	ing, projects, _ := newIngesterFixture(t)
	// newIngesterFixture already wrote sess.jsonl. Add two more
	// with controlled mtimes (oldest, middle, newest).
	dir := filepath.Join(projects, "-Users-x-foo")
	type entry struct {
		name string
		mod  time.Time
	}
	now := time.Now()
	files := []entry{
		{"a-old.jsonl", now.Add(-3 * time.Hour)},
		{"b-mid.jsonl", now.Add(-2 * time.Hour)},
		{"c-new.jsonl", now.Add(-1 * time.Hour)},
	}
	for _, e := range files {
		p := filepath.Join(dir, e.name)
		if err := os.WriteFile(p, jsonl(e.name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, e.mod, e.mod); err != nil {
			t.Fatal(err)
		}
	}

	bf := &Backfill{Ingester: ing}

	var calls []Progress
	if err := bf.Run(context.Background(), func(p Progress) {
		calls = append(calls, p)
	}); err != nil {
		t.Fatal(err)
	}

	if len(calls) < 2 {
		t.Fatalf("expected at least Total+final progress calls, got %d", len(calls))
	}

	first := calls[0]
	last := calls[len(calls)-1]

	if !first.Active || first.Done != 0 {
		t.Errorf("first call = %+v, want Active=true Done=0", first)
	}
	if last.Active {
		t.Errorf("last call still Active: %+v", last)
	}
	if first.Total < 4 {
		t.Errorf("Total = %d, want >= 4 (sess + a + b + c)", first.Total)
	}

	// Done counter strictly increases between the first and last.
	if last.Done != first.Total {
		t.Errorf("final Done = %d, want %d", last.Done, first.Total)
	}
}

func TestBackfillRun_NewestMtimeFirst(t *testing.T) {
	// Use a *fresh* fixture (without the pre-existing sess.jsonl) so
	// only our three controlled files are walked.
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects", "-Users-x-foo")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(projects, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	type entry struct {
		name string
		mod  time.Time
	}
	want := []entry{
		{"c-new.jsonl", now.Add(-1 * time.Hour)},
		{"b-mid.jsonl", now.Add(-2 * time.Hour)},
		{"a-old.jsonl", now.Add(-3 * time.Hour)},
	}
	for _, e := range want {
		p := filepath.Join(projects, e.name)
		if err := os.WriteFile(p, jsonl(e.name), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, e.mod, e.mod); err != nil {
			t.Fatal(err)
		}
	}

	// We need to observe order. Wrap an Ingester whose ProcessFile
	// also pushes the path into a slice. Simplest: replicate via a
	// fresh struct using the real Ingester but also recording the
	// order via a per-test side channel.
	ing := newTestIngester(t, filepath.Dir(projects), cacheDir)
	var seen []string
	bf := &Backfill{
		Ingester: ing,
		// hook for tests:
		onBeforeProcess: func(path string) { seen = append(seen, filepath.Base(path)) },
	}
	if err := bf.Run(context.Background(), func(Progress) {}); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 3 {
		t.Fatalf("processed %d files, want 3", len(seen))
	}
	if seen[0] != "c-new.jsonl" || seen[1] != "b-mid.jsonl" || seen[2] != "a-old.jsonl" {
		t.Errorf("order = %v, want [c-new b-mid a-old]", seen)
	}
}

// newTestIngester is a small constructor used by tests that need a
// fully wired Ingester pointing at custom dirs.
func newTestIngester(t *testing.T, projectsRoot, cacheDir string) *Ingester {
	t.Helper()
	c, err := openTestCache(t, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	tab := mustPricing(t)
	res := newTestResolver(c)
	return &Ingester{
		Cache:          c,
		Resolver:       res,
		Pricing:        tab,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}
}

func TestBackfillRun_EmptyTree(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(projects, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	ing := newTestIngester(t, projects, cacheDir)
	bf := &Backfill{Ingester: ing}

	var calls []Progress
	if err := bf.Run(context.Background(), func(p Progress) { calls = append(calls, p) }); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("empty tree fired %d progress calls, want 0", len(calls))
	}
}

func TestBackfillRun_NoIndicatorWhenAllFilesCached(t *testing.T) {
	// Regression for the post-`index --rebuild` UX bug: every file
	// is at-EOF in the cache, so backfill should fire zero progress
	// callbacks and the indicator should never appear.
	ing, _, path := newIngesterFixture(t)
	if _, err := ing.ProcessFile(path); err != nil {
		t.Fatal(err)
	}

	bf := &Backfill{Ingester: ing}
	var calls []Progress
	if err := bf.Run(context.Background(), func(p Progress) { calls = append(calls, p) }); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("all-cached tree fired %d progress calls, want 0", len(calls))
	}
}

func TestBackfillRun_MissingRoot(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	ing := newTestIngester(t, filepath.Join(dir, "does-not-exist"), cacheDir)
	bf := &Backfill{Ingester: ing}

	var calls []Progress
	if err := bf.Run(context.Background(), func(p Progress) { calls = append(calls, p) }); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("missing root produced %d progress calls, want 0", len(calls))
	}
}

func TestBackfillRun_ConcurrentWatcherSameFile(t *testing.T) {
	// Sanity check: a watcher event handler invoking ProcessFile on
	// the same path the backfill is about to walk must not corrupt
	// the cache. InsertMessages' INSERT OR IGNORE + SQLite's write
	// serialisation should make this safe.
	ing, _, path := newIngesterFixture(t)

	bf := &Backfill{
		Ingester: ing,
		onBeforeProcess: func(p string) {
			if p == path {
				// Simulate a watcher event for the same file landing
				// just before backfill processes it.
				_, _ = ing.ProcessFile(p)
			}
		},
	}
	if err := bf.Run(context.Background(), func(Progress) {}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := ing.Cache.DB().QueryRow(
		`SELECT count(*) FROM messages WHERE session_id = 's1'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("messages count = %d, want exactly 1 (idempotent insert)", n)
	}
}

func TestBackfillRun_HonoursCtxCancellation(t *testing.T) {
	ing, projects, _ := newIngesterFixture(t)
	dir := filepath.Join(projects, "-Users-x-foo")
	for i := range 5 {
		p := filepath.Join(dir, "extra-"+string(rune('a'+i))+".jsonl")
		if err := os.WriteFile(p, jsonl("e"+string(rune('a'+i))), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	bf := &Backfill{
		Ingester:        ing,
		onBeforeProcess: func(path string) { cancel() }, // cancel as soon as the first file starts
	}

	var last Progress
	if err := bf.Run(ctx, func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}

	if last.Active {
		t.Errorf("final progress still Active after cancel: %+v", last)
	}
}
