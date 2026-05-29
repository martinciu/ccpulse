package cache

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func TestAcquireCacheLock_FreshFileShared(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	f, err := acquireCacheLock(lockPath, syscall.LOCK_SH)
	if err != nil {
		t.Fatalf("acquireCacheLock(SH) on fresh file: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if f == nil {
		t.Fatal("acquireCacheLock returned nil *os.File without error")
	}
}

func TestAcquireCacheLock_SharedSharedCoexist(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	a, err := acquireCacheLock(lockPath, syscall.LOCK_SH)
	if err != nil {
		t.Fatalf("first SH: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := acquireCacheLock(lockPath, syscall.LOCK_SH)
	if err != nil {
		t.Fatalf("second SH: %v", err)
	}
	t.Cleanup(func() { b.Close() })
}

func TestAcquireCacheLock_SharedThenExclusiveRefused(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	a, err := acquireCacheLock(lockPath, syscall.LOCK_SH)
	if err != nil {
		t.Fatalf("first SH: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	_, err = acquireCacheLock(lockPath, syscall.LOCK_EX)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second EX while SH held: got %v, want ErrLockHeld", err)
	}
}

func TestAcquireCacheLock_ExclusiveThenSharedRefused(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	a, err := acquireCacheLock(lockPath, syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("first EX: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	_, err = acquireCacheLock(lockPath, syscall.LOCK_SH)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second SH while EX held: got %v, want ErrLockHeld", err)
	}
}

func TestAcquireCacheLock_ExclusiveThenExclusiveRefused(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	a, err := acquireCacheLock(lockPath, syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("first EX: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	_, err = acquireCacheLock(lockPath, syscall.LOCK_EX)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second EX: got %v, want ErrLockHeld", err)
	}
}

func TestAcquireCacheLock_ReleaseOnClose(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.db.lock")
	a, err := acquireCacheLock(lockPath, syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("first EX: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	b, err := acquireCacheLock(lockPath, syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("second EX after Close: %v", err)
	}
	t.Cleanup(func() { b.Close() })
}

func TestOpen_TwoSharedOpensCoexist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	a, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	b, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { b.Close() })
}

func TestOpen_RecordsLockFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if c.lockFile == nil {
		t.Fatal("Cache.lockFile is nil after Open")
	}
}

func TestClose_ReleasesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// EX should succeed because Close released SH.
	f, err := acquireCacheLock(path+".lock", syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("EX after Close: %v", err)
	}
	t.Cleanup(func() { f.Close() })
}

func TestLockedRebuild_FreshPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	c, err := LockedRebuild(t.Context(), path)
	if err != nil {
		t.Fatalf("LockedRebuild on fresh path: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatalf("count(*) after fresh rebuild: %v", err)
	}
	if n != 0 {
		t.Fatalf("messages count after fresh rebuild = %d, want 0", n)
	}
}

func TestLockedRebuild_RemovesSiblings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	// Seed a DB so siblings exist.
	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	tab, _ := pricing.Load()
	if err := c.InsertMessages(t.Context(), []parse.Message{{
		SessionID:   "seed",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	c2, err := LockedRebuild(t.Context(), path)
	if err != nil {
		t.Fatalf("LockedRebuild: %v", err)
	}
	t.Cleanup(func() { c2.Close() })

	var n int
	if err := c2.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatalf("count(*) after rebuild: %v", err)
	}
	if n != 0 {
		t.Fatalf("messages after rebuild = %d, want 0", n)
	}
}

func TestLockedRebuild_RefusedWhenSharedHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	holder, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("holder Open: %v", err)
	}
	t.Cleanup(func() { holder.Close() })

	_, err = LockedRebuild(t.Context(), path)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("LockedRebuild while SH held: got %v, want ErrLockHeld", err)
	}
}

func TestOpen_RefusedWhenExclusiveHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	// Hold EX directly via the helper to simulate a rebuild in progress.
	holderFD, err := acquireCacheLock(path+".lock", syscall.LOCK_EX)
	if err != nil {
		t.Fatalf("acquire EX holder: %v", err)
	}
	t.Cleanup(func() { holderFD.Close() })

	_, err = Open(t.Context(), path)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Open while EX held: got %v, want ErrLockHeld", err)
	}
}

func TestLockedRebuild_DowngradesToShared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := LockedRebuild(t.Context(), path)
	if err != nil {
		t.Fatalf("LockedRebuild: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// After LockedRebuild returns, another Open(SH) must succeed —
	// proving the lock was downgraded from EX to SH.
	c2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open after LockedRebuild: %v", err)
	}
	t.Cleanup(func() { c2.Close() })
}

// TestOpen_SchemaMismatch_LosesToConcurrentHolder covers the dispatch
// path Open → errSchemaMismatch → LockedRebuild → ErrLockHeld under
// concurrent SH-holder contention. Open releases its own SH fd before
// calling LockedRebuild, so LockedRebuild's LOCK_EX|LOCK_NB acquire
// must fail against the unrelated SH holder and surface as ErrLockHeld
// to the caller — never errSchemaMismatch, never a wrapped
// EWOULDBLOCK, never a panic. See issue #243.
func TestOpen_SchemaMismatch_LosesToConcurrentHolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	// Seed schema_version='0' on disk. Mirrors the
	// TestOpenWipesOnSchemaVersionMismatch pattern in cache_test.go.
	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := c.DB().ExecContext(t.Context(), `UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("seed UPDATE: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	// Hold LOCK_SH on the lock file from a fixture fd. BSD flock is
	// fd-scoped on darwin and linux, so an independent open() in this
	// same process contends identically to a separate process.
	holder, err := acquireCacheLock(path+".lock", syscall.LOCK_SH)
	if err != nil {
		t.Fatalf("acquire holder SH: %v", err)
	}
	t.Cleanup(func() { holder.Close() })

	// Open succeeds on its own SH (multiple SH coexist), openDB
	// returns errSchemaMismatch, Open releases its SH, LockedRebuild
	// attempts LOCK_EX with LOCK_NB → blocked by holder → ErrLockHeld.
	_, err = Open(t.Context(), path)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Open with schema mismatch + concurrent SH holder: got %v, want ErrLockHeld", err)
	}
}

func TestLockedRebuild_PreservesUsageSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	u := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 42.5}}
	when := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
		t.Fatalf("seed RecordUsageSample: %v", err)
	}
	// Force a schema mismatch so the next Open dispatches to LockedRebuild.
	if _, err := c.DB().ExecContext(t.Context(), `UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("seed UPDATE: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	c2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen (rebuild): %v", err)
	}
	defer c2.Close()

	var n int
	var pct float64
	if err := c2.DB().QueryRowContext(t.Context(),
		`SELECT count(*), COALESCE(MAX(five_hour_pct),0) FROM usage_samples`).Scan(&n, &pct); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("usage_samples count after rebuild = %d, want 1 (preserved)", n)
	}
	if pct != 42.5 {
		t.Errorf("five_hour_pct after rebuild = %v, want 42.5", pct)
	}
}

func TestLockedRebuild_PreservesMetaExceptSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := c.DB().ExecContext(t.Context(),
		`INSERT OR REPLACE INTO meta(key,value) VALUES('last_recost_history_fingerprint','v1,v2')`); err != nil {
		t.Fatalf("seed meta insert: %v", err)
	}
	if _, err := c.DB().ExecContext(t.Context(), `UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("seed UPDATE: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	c2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen (rebuild): %v", err)
	}
	defer c2.Close()

	var fp string
	if err := c2.DB().QueryRowContext(t.Context(),
		`SELECT value FROM meta WHERE key = 'last_recost_history_fingerprint'`).Scan(&fp); err != nil {
		t.Fatalf("fingerprint missing after rebuild: %v", err)
	}
	if fp != "v1,v2" {
		t.Errorf("fingerprint = %q, want v1,v2 (preserved)", fp)
	}

	var sv string
	if err := c2.DB().QueryRowContext(t.Context(),
		`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&sv); err != nil {
		t.Fatal(err)
	}
	if sv != SchemaVersion {
		t.Errorf("schema_version = %q, want %q (fresh, not the seeded 0)", sv, SchemaVersion)
	}
}
