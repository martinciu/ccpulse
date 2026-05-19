package cache

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
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
