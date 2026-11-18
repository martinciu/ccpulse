package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/spf13/cobra"
)

// Bounds on the statusline payload `status --stdin` reads (#568). A real
// payload is ~2 KB; ccstatusline gives a custom command 2000 ms in total, so
// a caller that never closes the pipe must not be able to hold the
// statusline hostage.
const (
	statuslineStdinLimit   = 1 << 20 // 1 MiB
	statuslineStdinTimeout = 500 * time.Millisecond
)

var (
	errStdinTooLarge = errors.New("stdin exceeds the size limit")
	errStdinTimeout  = errors.New("timed out waiting for stdin to close")
)

// readStdinBounded reads r to EOF, failing with errStdinTooLarge past limit
// bytes and with errStdinTimeout when EOF hasn't arrived within timeout.
//
// The read runs in a goroutine so the wait can be bounded. After a timeout
// that goroutine stays blocked on r until r is closed or the process exits —
// acceptable for the short-lived `ccpulse status`, the only caller.
func readStdinBounded(r io.Reader, limit int64, timeout time.Duration) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1) // buffered: a late reader never blocks on send
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, limit+1))
		done <- result{b, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			return nil, res.err
		}
		if int64(len(res.b)) > limit {
			return nil, errStdinTooLarge
		}
		return res.b, nil
	case <-timer.C:
		return nil, errStdinTimeout
	}
}

// errStdinTerminal is the one `--stdin` failure that changes the exit code:
// an interactive stdin would otherwise block until the timeout for a payload
// that is never coming.
var errStdinTerminal = errors.New("--stdin: stdin is a terminal; pipe Claude Code's statusline JSON into it")

// ingestStatuslineStdin reads Claude Code's statusline payload from the
// command's stdin and merges its rate_limits into the overlay. It returns an
// error only for a terminal stdin: malformed, missing or oversized input
// must never fail the statusline, so every other outcome is one DEBUG line
// and nil.
//
// Privacy: the payload carries cwd, transcript_path, session_id, workspace
// and model details. The DEBUG line carries only outcome, reason and the
// bucket count — never payload bytes, and never an err value (a decode
// error can quote input, a stat error wraps the path).
func ingestStatuslineStdin(cmd *cobra.Command, cacheDir string, now time.Time) error {
	in := cmd.InOrStdin()
	if isCharDevice(in) {
		return errStdinTerminal
	}
	r, reason := readStatusline(in, now)
	if reason != "" {
		slog.Debug("ccpulse.statuslineIngest", "outcome", "skipped", "reason", reason, "buckets", 0)
		return nil
	}
	buckets := 0
	for _, bk := range []*anthro.Bucket{r.FiveHour, r.SevenDay} {
		if bk != nil {
			buckets++
		}
	}

	changed, err := anthro.IngestStatusline(cacheDir, r, now)
	if err != nil {
		reason := "write_failed"
		if errors.Is(err, anthro.ErrOverlayLockBusy) {
			reason = "lock_busy"
		}
		slog.Debug("ccpulse.statuslineIngest", "outcome", "skipped", "reason", reason, "buckets", buckets)
		return nil
	}
	outcome := "unchanged"
	if changed {
		outcome = "ingested"
	}
	slog.Debug("ccpulse.statuslineIngest", "outcome", outcome, "buckets", buckets)
	return nil
}

// isCharDevice reports whether r is an *os.File on a character device
// other than /dev/null — in practice a terminal, never a pipe from a
// statusline. /dev/null is excluded: tmux #(), cron and launchd hand it to
// commands, Go reopens a closed fd 0 as it, and it reads as empty input
// (EOF at once), which must not fail the command.
func isCharDevice(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

// readStatusline reads and parses the payload. A non-empty reason names why
// there is nothing to ingest; it is the only detail the log line carries.
func readStatusline(in io.Reader, now time.Time) (anthro.StatuslineReading, string) {
	b, err := readStdinBounded(in, statuslineStdinLimit, statuslineStdinTimeout)
	switch {
	case errors.Is(err, errStdinTooLarge):
		return anthro.StatuslineReading{}, "too_large"
	case errors.Is(err, errStdinTimeout):
		return anthro.StatuslineReading{}, "timeout"
	case err != nil:
		return anthro.StatuslineReading{}, "read_failed"
	case len(bytes.TrimSpace(b)) == 0:
		return anthro.StatuslineReading{}, "empty"
	}
	r, err := anthro.ParseStatusline(b, now)
	switch {
	case errors.Is(err, anthro.ErrNoStatuslineQuota):
		return anthro.StatuslineReading{}, "no_rate_limits"
	case err != nil:
		return anthro.StatuslineReading{}, "invalid_json"
	}
	return r, ""
}
