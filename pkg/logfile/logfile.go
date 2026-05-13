package logfile

import (
	"os"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

const MaxBytes = 10 * 1024 * 1024 // 10 MB

// OpenLogFile is var so tests can shadow it to count calls.
// O_APPEND is intentionally omitted: OpenRotated is called when the file
// must be truncated (size > MaxBytes), and O_APPEND would cause writes to
// always append at EOF regardless of seek position, defeating rotation.
var OpenLogFile = func(path string) (*os.File, error) {
	return secfile.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
}

// OpenRotated opens path for append, removing it first if it exceeds MaxBytes.
// Returns nil on any error; callers no-op silently (best-effort logging).
func OpenRotated(path string) *os.File {
	if info, err := os.Stat(path); err == nil && info.Size() > MaxBytes {
		_ = os.Remove(path)
	}
	f, err := OpenLogFile(path)
	if err != nil {
		return nil
	}
	return f
}