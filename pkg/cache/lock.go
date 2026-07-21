package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

// ErrLockHeld signals a non-blocking flock acquire failure. Callers
// check via errors.Is; subcommands map to a user-facing message and
// exit code 75 (EX_TEMPFAIL). See issue #219.
var ErrLockHeld = errors.New("cache lock held by another ccpulse process")

// acquireCacheLock opens lockPath via secfile.OpenFile (0600 with
// O_CREATE|O_RDWR) and calls syscall.Flock with mode | LOCK_NB.
// On EWOULDBLOCK / EAGAIN returns ErrLockHeld and emits a single
// slog.Warn line. On any other error returns the wrapped error.
//
// mode is syscall.LOCK_SH or syscall.LOCK_EX.
//
// The returned *os.File owns the lock; close it (or call Flock(LOCK_UN))
// to release.
func acquireCacheLock(lockPath string, mode int) (*os.File, error) {
	f, err := secfile.OpenFile(lockPath, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, fmt.Errorf("open cache lock %s: %w", lockPath, err)
	}
	//nolint:gosec // G115: f.Fd() returns uintptr; OS fds are small ints, no overflow
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		f.Close()
		// syscall.EWOULDBLOCK == syscall.EAGAIN on darwin and linux; checking either is sufficient.
		if errors.Is(err, syscall.EWOULDBLOCK) {
			slog.Warn("cache.lockHeld",
				"path", lockPath,
				"mode", lockModeLabel(mode))
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("flock %s: %w", lockPath, err)
	}
	return f, nil
}

// lockModeLabel maps the syscall.LOCK_* constant to a stable
// human-readable label for the slog "mode" attribute. Anything other
// than LOCK_SH / LOCK_EX is a programmer error.
func lockModeLabel(mode int) string {
	switch mode {
	case syscall.LOCK_SH:
		return "shared"
	case syscall.LOCK_EX:
		return "exclusive"
	default:
		return fmt.Sprintf("invalid(%d)", mode)
	}
}

// LockedRebuild acquires LOCK_EX on path+".lock", calls the
// (internal) removeWithSiblings, opens a fresh DB via openDB,
// then atomically downgrades the lock fd to LOCK_SH before
// returning. Cache.Close releases the lock.
//
// flock(2) guarantees atomic lock-mode conversion when called on
// an already-held fd, so no race window exists between unlock and
// re-acquire during the downgrade.
//
// Returns ErrLockHeld if any other process holds the lock.
// LockedRebuild is the ONLY legal way to unlink state.db outside
// of tests.
func LockedRebuild(ctx context.Context, path string) (*Cache, error) {
	lockFile, err := acquireCacheLock(path+".lock", syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	preserved := snapshotPreservable(ctx, path)
	if err := removeWithSiblings(path); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("rebuild remove: %w", err)
	}
	db, err := openDB(ctx, path)
	if err != nil {
		lockFile.Close()
		return nil, err
	}
	if err := restorePreservable(ctx, db, preserved); err != nil {
		// Best-effort: the messages table is the load-bearing one. Log and
		// continue rather than failing the rebuild over quota-history.
		slog.Warn("cache.preserveAcrossRebuild", "err", err)
	}
	// Atomic EX → SH downgrade. flock(2): "subsequent flock()
	// calls on an already locked file will convert an existing
	// lock to the new lock mode."
	//nolint:gosec // G115: f.Fd() returns uintptr; OS fds are small ints, no overflow
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_SH); err != nil {
		db.Close()
		lockFile.Close()
		return nil, fmt.Errorf("downgrade cache lock %s: %w", path+".lock", err)
	}
	return &Cache{db: db, lockFile: lockFile}, nil
}

// preservedTable is the column names plus row tuples read from a table
// that must survive a schema-bump rebuild.
type preservedTable struct {
	cols []string
	rows [][]any
}

// rebuildSnapshot holds the tables/keys carried across a LockedRebuild
// destroy+recreate — everything NOT derivable from transcripts. Today:
// the Anthropic quota-history (usage_samples, issue #22; usage_limits,
// issue #455) and meta rows other than schema_version (chiefly the
// recost fingerprint).
type rebuildSnapshot struct {
	usageSamples preservedTable
	usageLimits  preservedTable
	meta         preservedTable
}

// snapshotPreservable reads the preservable tables from the existing DB at
// path into memory, before removeWithSiblings deletes the file. Best-effort:
// a missing file (fresh launch) or any read error yields the zero value, so
// the rebuild proceeds with nothing preserved rather than failing.
//
// Takes no lock — the caller (LockedRebuild) already holds LOCK_EX on the
// .lock sibling, and flock guards the .lock file, not the DB file.
func snapshotPreservable(ctx context.Context, path string) rebuildSnapshot {
	var snap rebuildSnapshot
	if _, err := os.Stat(path); err != nil {
		return snap // fresh path — nothing to preserve, and don't create a phantom file
	}
	db, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		return snap
	}
	defer db.Close()
	snap.usageSamples = snapshotTable(ctx, db, "usage_samples", "")
	if tableExists(ctx, db, "usage_limits") {
		snap.usageLimits = snapshotTable(ctx, db, "usage_limits", "")
	}
	snap.meta = snapshotTable(ctx, db, "meta", "key <> 'schema_version'")
	return snap
}

// tableExists reports whether name is a table in db. Guards snapshotTable
// calls for tables added in later schema versions, so a rebuild from an
// older schema doesn't log a spurious cache.snapshotTableFailed warn for a
// table that legitimately doesn't exist yet.
func tableExists(ctx context.Context, db *sql.DB, name string) bool {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return err == nil && n > 0
}

// snapshotTable runs SELECT * (optionally filtered by where) and returns the
// column names and row tuples. table and where are compile-time constants
// from snapshotPreservable, never user input. Any error yields an empty
// preservedTable — preserve-all-or-nothing per table.
func snapshotTable(ctx context.Context, db *sql.DB, table, where string) preservedTable {
	//nolint:gosec // G202: table/where are compile-time constants from snapshotPreservable, not user input
	q := "SELECT * FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		slog.Warn("cache.snapshotTableFailed", "table", table, "err", err)
		return preservedTable{}
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		slog.Warn("cache.snapshotTableFailed", "table", table, "err", err)
		return preservedTable{}
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			slog.Warn("cache.snapshotTableFailed", "table", table, "err", err)
			return preservedTable{}
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("cache.snapshotTableFailed", "table", table, "err", err)
		return preservedTable{}
	}
	return preservedTable{cols: cols, rows: out}
}

// restorePreservable re-inserts the snapshot into the freshly-rebuilt DB.
// Best-effort and idempotent (INSERT OR REPLACE): a partial failure returns
// an error for the caller to log, but never blocks the rebuild.
func restorePreservable(ctx context.Context, db *sql.DB, snap rebuildSnapshot) error {
	if err := restoreTable(ctx, db, "usage_samples", snap.usageSamples); err != nil {
		return fmt.Errorf("restore usage_samples: %w", err)
	}
	if err := restoreTable(ctx, db, "usage_limits", snap.usageLimits); err != nil {
		return fmt.Errorf("restore usage_limits: %w", err)
	}
	if err := restoreTable(ctx, db, "meta", snap.meta); err != nil {
		return fmt.Errorf("restore meta: %w", err)
	}
	return nil
}

// restoreTable INSERT OR REPLACEs the preserved rows back into table. A no-op
// when there are no rows. table and pt.cols originate from snapshotTable
// (compile-time table name + the DB's own column names), never user input.
func restoreTable(ctx context.Context, db *sql.DB, table string, pt preservedTable) error {
	if len(pt.rows) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(pt.cols)), ",")
	//nolint:gosec // G201: table + column names come from the DB schema, not user input
	q := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
		table, strings.Join(pt.cols, ","), placeholders)
	stmt, err := db.PrepareContext(ctx, q)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, row := range pt.rows {
		if _, err := stmt.ExecContext(ctx, row...); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
	}
	return nil
}
