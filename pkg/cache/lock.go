package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	if err := removeWithSiblings(path); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("rebuild remove: %w", err)
	}
	db, err := openDB(ctx, path)
	if err != nil {
		lockFile.Close()
		return nil, err
	}
	// Atomic EX → SH downgrade. flock(2): "subsequent flock()
	// calls on an already locked file will convert an existing
	// lock to the new lock mode."
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_SH); err != nil {
		db.Close()
		lockFile.Close()
		return nil, fmt.Errorf("downgrade cache lock %s: %w", path+".lock", err)
	}
	return &Cache{db: db, lockFile: lockFile}, nil
}
