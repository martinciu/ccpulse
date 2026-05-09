package ingest

import (
	"os"
	"path/filepath"
	"strings"
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

func TestProcessFile_TailFromStoredOffset(t *testing.T) {
	ing, _, path := newIngesterFixture(t)

	// First pass — full parse, records offset.
	if _, err := ing.ProcessFile(path); err != nil {
		t.Fatal(err)
	}
	_, off1, _, _, _ := ing.Cache.GetFile(path)

	// Append a second assistant line to the same file.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(jsonl("s2")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Second pass — should tail-parse only the appended line.
	n, err := ing.ProcessFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("inserted on second pass = %d, want 1", n)
	}

	var total int
	if err := ing.Cache.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total messages = %d, want 2", total)
	}

	_, off2, _, _, _ := ing.Cache.GetFile(path)
	if off2 <= off1 {
		t.Errorf("offset did not advance: off1=%d off2=%d", off1, off2)
	}
}

func TestProcessFile_SkipsWhenAtEOF(t *testing.T) {
	ing, _, path := newIngesterFixture(t)

	// First pass: ingest fully.
	if _, err := ing.ProcessFile(path); err != nil {
		t.Fatal(err)
	}

	// Make the file unreadable AFTER recording the offset.
	// If ProcessFile attempts to open it, it will log a permission
	// error. If it skips correctly, the log stays empty.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	n, err := ing.ProcessFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("inserted on skip pass = %d, want 0", n)
	}

	if data, _ := os.ReadFile(ing.ParseErrorsLog); len(data) > 0 {
		t.Errorf("expected empty log (file should not have been opened), got: %s", data)
	}
}

func TestProcessFile_ResetsOnTruncation(t *testing.T) {
	ing, _, path := newIngesterFixture(t)

	// Pretend a previous run recorded a much larger offset.
	st, _ := os.Stat(path)
	if err := ing.Cache.RecordFile(path, st.ModTime().UnixNano(), st.Size()*10, 100); err != nil {
		t.Fatal(err)
	}

	n, err := ing.ProcessFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("inserted after truncation reset = %d, want 1", n)
	}

	_, off, line, _, _ := ing.Cache.GetFile(path)
	if off != st.Size() {
		t.Errorf("offset after reset = %d, want %d", off, st.Size())
	}
	if line != 1 {
		t.Errorf("line after reset = %d, want 1", line)
	}
}

func TestProcessFile_TagsSubagentMessages(t *testing.T) {
	ing, projects, _ := newIngesterFixture(t)

	subDir := filepath.Join(projects, "-Users-x-foo", "sid-abc", "subagents")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(subDir, "agent-1.jsonl")
	if err := os.WriteFile(subPath, jsonl("sub1"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ing.ProcessFile(subPath); err != nil {
		t.Fatal(err)
	}

	var isSub int
	var parent string
	if err := ing.Cache.DB().QueryRow(
		`SELECT is_subagent, parent_session_id FROM messages WHERE session_id = 'sub1'`,
	).Scan(&isSub, &parent); err != nil {
		t.Fatal(err)
	}
	if isSub != 1 {
		t.Errorf("is_subagent = %d, want 1", isSub)
	}
	if parent != "sid-abc" {
		t.Errorf("parent_session_id = %q, want sid-abc", parent)
	}
}

func TestProcessFile_TopLevelHasNoSubagentTag(t *testing.T) {
	ing, _, path := newIngesterFixture(t)
	if _, err := ing.ProcessFile(path); err != nil {
		t.Fatal(err)
	}

	var isSub int
	var parent string
	if err := ing.Cache.DB().QueryRow(
		`SELECT is_subagent, parent_session_id FROM messages WHERE session_id = 's1'`,
	).Scan(&isSub, &parent); err != nil {
		t.Fatal(err)
	}
	if isSub != 0 || parent != "" {
		t.Errorf("top-level message tagged as subagent: isSub=%d parent=%q", isSub, parent)
	}
}

func TestProcessFile_BackfillsCanonical(t *testing.T) {
	ing, _, path := newIngesterFixture(t)

	// Pre-seed the slug_canonical table so the resolver returns
	// a concrete CanonicalPath without needing a real git repo.
	if err := ing.Cache.PutSlugCanonical(cache.SlugCanonical{
		Slug:          "-Users-x-foo",
		CanonicalPath: "/Users/x/foo",
		Branch:        "main",
		Resolved:      true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ing.ProcessFile(path); err != nil {
		t.Fatal(err)
	}

	var canon, branch string
	if err := ing.Cache.DB().QueryRow(
		`SELECT project_canonical, worktree_branch FROM messages WHERE session_id = 's1'`,
	).Scan(&canon, &branch); err != nil {
		t.Fatal(err)
	}
	if canon != "/Users/x/foo" {
		t.Errorf("project_canonical = %q, want /Users/x/foo", canon)
	}
	if branch != "main" {
		t.Errorf("worktree_branch = %q, want main", branch)
	}
}

func TestProcessFile_MissingFileLogsAndReturnsZero(t *testing.T) {
	ing, projects, _ := newIngesterFixture(t)

	missing := filepath.Join(projects, "-Users-x-foo", "ghost.jsonl")
	n, err := ing.ProcessFile(missing)
	if err != nil {
		t.Fatalf("ProcessFile on missing file returned err: %v", err)
	}
	if n != 0 {
		t.Errorf("inserted on missing = %d, want 0", n)
	}

	logBytes, _ := os.ReadFile(ing.ParseErrorsLog)
	if !strings.Contains(string(logBytes), "ghost.jsonl") {
		t.Errorf("expected log to mention ghost.jsonl, got: %s", logBytes)
	}
}

func TestProcessFile_MalformedLineLoggedValidLinesInserted(t *testing.T) {
	ing, projects, _ := newIngesterFixture(t)

	mixedPath := filepath.Join(projects, "-Users-x-foo", "mixed.jsonl")
	contents := append(jsonl("good1"), []byte("{not valid json\n")...)
	contents = append(contents, jsonl("good2")...)
	if err := os.WriteFile(mixedPath, contents, 0644); err != nil {
		t.Fatal(err)
	}

	n, err := ing.ProcessFile(mixedPath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("inserted = %d, want 2 (the two valid lines)", n)
	}

	logBytes, _ := os.ReadFile(ing.ParseErrorsLog)
	if !strings.Contains(string(logBytes), "mixed.jsonl:2") {
		t.Errorf("expected log to mention mixed.jsonl:2 (the bad line), got: %s", logBytes)
	}
}

// helpers shared with backfill_test.go

func openTestCache(t *testing.T, cacheDir string) (*cache.Cache, error) {
	t.Helper()
	c, err := cache.Open(filepath.Join(cacheDir, "state.db"))
	if err == nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

func mustPricing(t *testing.T) pricing.Table {
	t.Helper()
	tab, err := pricing.Load()
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func newTestResolver(c *cache.Cache) *canonical.Resolver {
	return canonical.NewResolver(c, "/")
}
