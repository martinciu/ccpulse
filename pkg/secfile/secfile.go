// Package secfile provides file/dir helpers that enforce private modes
// (0700 for dirs, 0600 for files) and chmod existing entries to those
// modes on next access.
//
// Callers must pass paths that ccpulse owns (e.g. ~/.cache/ccpulse,
// not ~/.cache). Pre-existing parent dirs created by the user or
// the OS are not chmod'd. The chmod step uses os.Chmod, which follows
// symlinks; a hostile symlink at the leaf path would chmod its target,
// so callers must own the entire path tree.
package secfile

import (
	"os"
	"path/filepath"
)

const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// MkdirAll creates dir (and any missing parents) at DirMode, then
// chmods dir to DirMode so a pre-existing dir at a looser mode is
// tightened. Only the leaf dir passed in is chmod'd; any pre-existing
// parents are left alone.
func MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return os.Chmod(dir, DirMode)
}

// WriteFile is os.WriteFile with FileMode, followed by os.Chmod to
// tighten a pre-existing file.
//
// If chmod fails after a successful write, the new contents are
// already on disk; the caller sees only the chmod error.
func WriteFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, FileMode); err != nil {
		return err
	}
	return os.Chmod(path, FileMode)
}

// OpenFile is os.OpenFile with FileMode, followed by os.Chmod after
// open to tighten a pre-existing file. The caller owns closing.
//
// If chmod fails after a successful open, the file may remain on disk
// (created by O_CREATE at FileMode); the caller sees only the chmod
// error.
func OpenFile(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, FileMode)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, FileMode); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// WriteFileAtomic writes data to path via a unique temp file in the same
// directory followed by os.Rename. POSIX rename is atomic on the same
// filesystem, so concurrent readers always see either the previous
// complete file or the new complete file — never a partial one.
//
// On Unix os.CreateTemp creates the temp at FileMode (0600); rename
// replaces the destination inode with the temp's inode, so the
// destination ends up at FileMode without a post-rename chmod.
//
// If marshalling/writing/renaming fails, the temp is removed
// best-effort. After a successful rename the temp path no longer
// exists, so the deferred Remove is a harmless no-op.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
