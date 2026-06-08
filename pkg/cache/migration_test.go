package cache

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestOpen_UpgradesFromV7_RebuildsWithRepoRoot reproduces the #408 schema bump.
// An existing pre-v8 cache has a messages table WITHOUT repo_root and
// schema_version='7'. Opening it must rebuild to v8 — NOT crash applying the
// new CREATE INDEX ON messages(ts, repo_root) over the old table (which lacks
// the column). Quota history (usage_samples) must survive the rebuild.
//
// Regression guard: every prior test opened a FRESH DB, so none exercised the
// upgrade path; the broken openDB applied schemaSQL before the version check
// and failed with "no such column: repo_root" before rebuild could dispatch.
func TestOpen_UpgradesFromV7_RebuildsWithRepoRoot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// Hand-build a v7-era DB: messages WITHOUT repo_root, the version row pinned
	// to '7', and one usage_samples row to prove quota history is preserved.
	old, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`CREATE TABLE messages (
			id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, message_id TEXT NOT NULL,
			project_slug TEXT NOT NULL, ts TEXT NOT NULL, role TEXT NOT NULL, model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
			cache_read_tokens INTEGER NOT NULL, cache_write_5m_tokens INTEGER NOT NULL,
			cache_write_1h_tokens INTEGER NOT NULL, cost_usd_estimate REAL NOT NULL,
			pricing_version TEXT NOT NULL, pricing_unknown INTEGER NOT NULL DEFAULT 0,
			is_subagent INTEGER NOT NULL DEFAULT 0, parent_session_id TEXT,
			cwd TEXT NOT NULL DEFAULT '', git_branch TEXT NOT NULL DEFAULT '',
			UNIQUE(session_id, message_id))`,
		`CREATE INDEX idx_messages_ts ON messages(ts)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','7')`,
		`CREATE TABLE usage_samples (ts INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT 'api', five_hour_pct REAL)`,
		`INSERT INTO usage_samples(ts, five_hour_pct) VALUES(1700000000, 42.5)`,
		`INSERT INTO messages(session_id,message_id,project_slug,ts,role,model,input_tokens,output_tokens,cache_read_tokens,cache_write_5m_tokens,cache_write_1h_tokens,cost_usd_estimate,pricing_version)
		 VALUES('s','m','slug','2026-01-01T00:00:00.000Z','assistant','claude',1,1,0,0,0,0.0,'v1')`,
	}
	for _, s := range seed {
		if _, err := old.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed v7 db: %v\nstmt: %s", err, s)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// Must rebuild, not crash with "no such column: repo_root".
	c, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on v7 cache must rebuild, got: %v", err)
	}
	defer c.Close()

	var n int
	if err := c.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='repo_root'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages.repo_root column count = %d, want 1 after upgrade", n)
	}

	var ver string
	if err := c.DB().QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "8" {
		t.Fatalf("schema_version = %q, want 8 after upgrade", ver)
	}

	// Quota history preserved across the destroy+recreate rebuild.
	var pct float64
	if err := c.DB().QueryRowContext(ctx,
		`SELECT five_hour_pct FROM usage_samples WHERE ts=1700000000`).Scan(&pct); err != nil {
		t.Fatalf("usage_samples not preserved across rebuild: %v", err)
	}
	if pct != 42.5 {
		t.Fatalf("preserved five_hour_pct = %v, want 42.5", pct)
	}
}
