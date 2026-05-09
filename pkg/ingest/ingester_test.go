package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// jsonl returns a single assistant-line transcript with the given
// session id. Used throughout the ingest tests.
func jsonl(sid string) []byte {
	return []byte(`{"type":"assistant","sessionId":"` + sid +
		`","timestamp":"2026-05-09T10:00:00.000Z","message":` +
		`{"role":"assistant","model":"claude-opus-4-7","usage":` +
		`{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}` +
		"\n")
}

// newIngesterFixture builds an Ingester pointed at temp dirs, plus
// a transcript file ready to ingest. Returns (ingester, projectsRoot,
// jsonlPath, cleanup-via-t).
func newIngesterFixture(t *testing.T) (*Ingester, string, string) {
	t.Helper()
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(projects, "-Users-x-foo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	c, err := cache.Open(filepath.Join(cacheDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	tab, _ := pricing.Load()
	res := canonical.NewResolver(c, "/")

	jsonlPath := filepath.Join(projects, "-Users-x-foo", "sess.jsonl")
	if err := os.WriteFile(jsonlPath, jsonl("s1"), 0644); err != nil {
		t.Fatal(err)
	}

	ing := &Ingester{
		Cache:          c,
		Resolver:       res,
		Pricing:        tab,
		ProjectsRoot:   projects,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}
	return ing, projects, jsonlPath
}

func TestProcessFile_NewFileFullParse(t *testing.T) {
	ing, _, path := newIngesterFixture(t)

	n, err := ing.ProcessFile(path)
	if err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1", n)
	}

	var count int
	if err := ing.Cache.DB().QueryRow(
		`SELECT count(*) FROM messages WHERE session_id = 's1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("messages count = %d, want 1", count)
	}

	_, off, line, found, err := ing.Cache.GetFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("files row not recorded")
	}
	st, _ := os.Stat(path)
	if off != st.Size() {
		t.Errorf("recorded offset = %d, want file size %d", off, st.Size())
	}
	if line != 1 {
		t.Errorf("recorded line = %d, want 1", line)
	}
}
