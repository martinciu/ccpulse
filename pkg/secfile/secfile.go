// Package secfile provides file/dir helpers that enforce private modes
// (0700 for dirs, 0600 for files) and chmod existing entries to those
// modes on next access.
//
// Callers must pass paths that ccpulse owns (e.g. ~/.cache/ccpulse,
// not ~/.cache). Pre-existing parent dirs created by the user or
// the OS are not chmod'd.
package secfile

import "os"

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
func WriteFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, FileMode); err != nil {
		return err
	}
	return os.Chmod(path, FileMode)
}

// OpenFile is os.OpenFile with FileMode, followed by os.Chmod after
// open to tighten a pre-existing file. The caller owns closing.
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
