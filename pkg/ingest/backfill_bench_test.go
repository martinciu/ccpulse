package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// BenchmarkBackfillEnumerate1k measures the enumeration cost of
// Backfill.Run when every file in a 1000-file tree is already at-EOF
// in the cache. The work loop is short-circuited, so this benchmark
// isolates the filter step: WalkDir + the new batched-cursor map
// lookup. Not a CI regression gate — a reproducible probe.
//
// Run with: go test ./pkg/ingest/ -bench BenchmarkBackfillEnumerate1k -run ^$ -count=10
func BenchmarkBackfillEnumerate1k(b *testing.B) {
	b.ReportAllocs()

	dir := b.TempDir()
	projects := filepath.Join(dir, "projects", "-Users-x-foo")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Fatal(err)
	}

	// Build 1000 .jsonl files and pre-record each one in the cache
	// at its exact on-disk size. The Backfill visitor should filter
	// every one of them via the batched map; the work loop never
	// fires, so the benchmark measures the filter step only.
	ing := newBenchIngester(b, filepath.Dir(projects), cacheDir)
	const n = 1000
	for i := range n {
		name := fmt.Sprintf("f-%04d.jsonl", i)
		p := filepath.Join(projects, name)
		if err := os.WriteFile(p, jsonl(name), 0o644); err != nil {
			b.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			b.Fatal(err)
		}
		if err := ing.Cache.RecordFile(p, info.ModTime().UnixNano(), info.Size(), 1); err != nil {
			b.Fatal(err)
		}
	}

	bf := &Backfill{Ingester: ing}

	for b.Loop() {
		if err := bf.Run(context.Background(), nil); err != nil {
			b.Fatal(err)
		}
	}
}

// newBenchIngester mirrors newTestIngester for *testing.B. The two
// helpers are kept separate because newTestIngester takes *testing.T;
// duplicating the half-dozen lines is cheaper than wrapping testing.B
// in an adapter.
func newBenchIngester(b *testing.B, projectsRoot, cacheDir string) *Ingester {
	b.Helper()
	c, err := cache.Open(filepath.Join(cacheDir, "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { c.Close() })

	tab, _ := pricing.Load()
	return &Ingester{
		Cache:          c,
		Pricing:        tab,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}
}
