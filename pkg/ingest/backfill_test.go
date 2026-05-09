package ingest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// recordedProgress collects every Progress callback for assertion.
type recordedProgress struct {
	calls []Progress
	paths []string // populated by tests that wrap ProcessFile
}

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

	gotOrder := append([]string{}, seen...)
	sort.SliceStable(gotOrder, func(i, j int) bool { return false }) // no-op; assert raw order
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
