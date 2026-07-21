package cache

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
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
	if ver != "11" {
		t.Fatalf("schema_version = %q, want 11 after upgrade", ver)
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

// TestOpen_UpgradesFromV8_PreservesUsageHistory covers the #455 v8→v9 bump:
// a v8 cache (no usage_limits table) must rebuild to v9 with quota history
// intact and an empty usage_limits table created.
func TestOpen_UpgradesFromV8_PreservesUsageHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	old, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','8')`,
		`CREATE TABLE usage_samples (ts INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT 'api', five_hour_pct REAL)`,
		`INSERT INTO usage_samples(ts, five_hour_pct) VALUES(1700000000, 42.5)`,
	}
	for _, s := range seed {
		if _, err := old.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed v8 db: %v\nstmt: %s", err, s)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on v8 cache must rebuild, got: %v", err)
	}
	defer c.Close()

	var ver string
	if err := c.DB().QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "11" {
		t.Fatalf("schema_version = %q, want 11 after upgrade", ver)
	}

	var pct float64
	if err := c.DB().QueryRowContext(ctx,
		`SELECT five_hour_pct FROM usage_samples WHERE ts=1700000000`).Scan(&pct); err != nil {
		t.Fatalf("usage_samples not preserved across rebuild: %v", err)
	}
	if pct != 42.5 {
		t.Fatalf("preserved five_hour_pct = %v, want 42.5", pct)
	}

	var n int
	if err := c.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_limits`).Scan(&n); err != nil {
		t.Fatalf("usage_limits table missing after upgrade: %v", err)
	}
	if n != 0 {
		t.Fatalf("usage_limits rows = %d, want 0 (no history existed)", n)
	}
}

// TestOpen_UpgradesFromV9_PreservesUsageLimitsWithEmptyScopeModelID covers
// the #459 v9→v10 bump: a v9 usage_limits table (no scope_model_id column)
// rebuilt to v10 must preserve its rows, with scope_model_id defaulting to
// ” — restoreTable (lock.go) inserts using the OLD db's own column list, so
// the new column is never named in the INSERT and falls back to its DDL
// default rather than erroring.
func TestOpen_UpgradesFromV9_PreservesUsageLimitsWithEmptyScopeModelID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	old, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','9')`,
		`CREATE TABLE usage_samples (ts INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT 'api', five_hour_pct REAL)`,
		`INSERT INTO usage_samples(ts, five_hour_pct) VALUES(1700000000, 42.5)`,
		`CREATE TABLE usage_limits (
			ts            INTEGER NOT NULL,
			kind          TEXT    NOT NULL,
			lim_group     TEXT    NOT NULL DEFAULT '',
			percent       REAL    NOT NULL DEFAULT 0,
			severity      TEXT    NOT NULL DEFAULT '',
			resets_at     TEXT,
			scope_model   TEXT    NOT NULL DEFAULT '',
			scope_surface TEXT    NOT NULL DEFAULT '',
			is_active     INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (ts, kind, scope_model, scope_surface)
		)`,
		`INSERT INTO usage_limits(ts, kind, lim_group, percent, severity, scope_model, is_active)
		 VALUES(1700000000, 'weekly_scoped', 'weekly', 35, 'normal', 'Fable', 1)`,
	}
	for _, s := range seed {
		if _, err := old.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed v9 db: %v\nstmt: %s", err, s)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on v9 cache must rebuild, got: %v", err)
	}
	defer c.Close()

	var ver string
	if err := c.DB().QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "11" {
		t.Fatalf("schema_version = %q, want 11 after upgrade", ver)
	}

	var pct float64
	if err := c.DB().QueryRowContext(ctx,
		`SELECT five_hour_pct FROM usage_samples WHERE ts=1700000000`).Scan(&pct); err != nil {
		t.Fatalf("usage_samples not preserved across rebuild: %v", err)
	}
	if pct != 42.5 {
		t.Fatalf("preserved five_hour_pct = %v, want 42.5", pct)
	}

	var scopeModel, scopeModelID string
	var limPct float64
	if err := c.DB().QueryRowContext(ctx,
		`SELECT scope_model, scope_model_id, percent FROM usage_limits WHERE ts=1700000000 AND kind='weekly_scoped'`).
		Scan(&scopeModel, &scopeModelID, &limPct); err != nil {
		t.Fatalf("usage_limits not preserved across rebuild: %v", err)
	}
	if scopeModel != "Fable" || limPct != 35 {
		t.Fatalf("preserved row scope_model=%q percent=%v, want Fable/35", scopeModel, limPct)
	}
	if scopeModelID != "" {
		t.Fatalf("preserved row scope_model_id=%q, want empty (v9 db had no such column)", scopeModelID)
	}
}

// TestLockedRebuild_PreservesUsageLimits proves limits history survives the
// destroy+recreate rebuild at the current schema version (v10 as of #459).
func TestLockedRebuild_PreservesUsageLimits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	fable := "Fable"
	u := anthro.Usage{Limits: []anthro.Limit{
		{
			Kind: "weekly_scoped", Group: "weekly", Percent: 35, Severity: "normal", IsActive: true,
			Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: &fable}},
		},
	}}
	if err := c.RecordUsageSample(ctx, u, time.Unix(1750000000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := LockedRebuild(ctx, path)
	if err != nil {
		t.Fatalf("LockedRebuild: %v", err)
	}
	defer c2.Close()

	var scopeModel string
	var pct float64
	if err := c2.DB().QueryRowContext(ctx,
		`SELECT scope_model, percent FROM usage_limits WHERE ts=1750000000 AND kind='weekly_scoped'`).
		Scan(&scopeModel, &pct); err != nil {
		t.Fatalf("usage_limits not preserved across rebuild: %v", err)
	}
	if scopeModel != "Fable" || pct != 35 {
		t.Fatalf("preserved row scope_model=%q percent=%v, want Fable/35", scopeModel, pct)
	}
}

// TestOpen_UpgradesFromV10_AddsEffortAndIterations covers the #457 v10→v11
// bump: a v10 cache (messages without effort / iterations_json) must rebuild
// to v11 with both columns present and quota history preserved.
func TestOpen_UpgradesFromV10_AddsEffortAndIterations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

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
			repo_root TEXT NOT NULL DEFAULT '',
			UNIQUE(session_id, message_id))`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','10')`,
		`CREATE TABLE usage_samples (ts INTEGER PRIMARY KEY, source TEXT NOT NULL DEFAULT 'api', five_hour_pct REAL)`,
		`INSERT INTO usage_samples(ts, five_hour_pct) VALUES(1700000000, 42.5)`,
	}
	for _, s := range seed {
		if _, err := old.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed v10 db: %v\nstmt: %s", err, s)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on v10 cache must rebuild, got: %v", err)
	}
	defer c.Close()

	var ver string
	if err := c.DB().QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "11" {
		t.Fatalf("schema_version = %q, want 11 after upgrade", ver)
	}

	var n int
	if err := c.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name IN ('effort','iterations_json')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("new column count = %d, want 2 (effort, iterations_json)", n)
	}

	var pct float64
	if err := c.DB().QueryRowContext(ctx,
		`SELECT five_hour_pct FROM usage_samples WHERE ts=1700000000`).Scan(&pct); err != nil {
		t.Fatalf("usage_samples not preserved across rebuild: %v", err)
	}
	if pct != 42.5 {
		t.Fatalf("preserved five_hour_pct = %v, want 42.5", pct)
	}
}
