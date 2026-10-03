package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
)

const (
	// admissionWindow is how far back, by file mtime, doctor re-parses
	// transcripts. A week covers days without Claude Code use; on the busiest
	// real week measured it is ~620 files / ~340 MB / ~2 s.
	admissionWindow = 7 * 24 * time.Hour
	// refusalRateLimit is the refused share at which the line turns ✗. The real
	// baseline is exactly 0, and a genuine format change lands near 100%.
	refusalRateLimit = 0.01
	// refusalMinLines keeps "1 of 3 lines" on a quiet week from tripping ✗.
	refusalMinLines = 5
)

// reportAdmission re-parses every transcript modified in the last
// admissionWindow and reports how many assistant lines the parser refused
// (missing envelope, unusable timestamp, or undecodable), #532.
//
// Unlike reportParseErrors, this line grades: ✗ once refusals reach
// refusalRateLimit of all assistant lines (with at least refusalMinLines
// refused). parse-errors.log mixes every class of record and cannot be told
// apart without classifying each one; here every refusal is classified by
// sentinel, and the whole point of #532 is that a quiet number is too quiet —
// a stricter admission rule that silently stops counting real usage after a
// Claude Code format change must say so loudly.
//
// It re-parses live rather than reading a log or a stored counter: an
// `index --rebuild` (or a truncation reset) re-logs every refusal with a fresh
// date, so neither a timestamped parse-errors.log nor persisted counters could
// tell a spike from a replay. The re-parse is stateless and runs the exact code
// ingest runs.
//
// Output is counts only — never paths, file names or line content. Files that
// cannot be read are skipped and counted; the walk itself never aborts, except
// when ctx is cancelled (Ctrl-C), which prints "scan interrupted" instead of a
// verdict over part of the window.
func reportAdmission(ctx context.Context, out io.Writer, projectsRoot string, now time.Time) {
	const prefix = "transcript admission (7d): "
	sum, unreadable, err := scanAdmissionWindow(ctx, projectsRoot, now.Add(-admissionWindow))
	if err != nil {
		fmt.Fprintln(out, "ℹ "+prefix+"scan interrupted")
		return
	}

	suffix := ""
	switch {
	case unreadable == 1:
		suffix = " (1 file unreadable)"
	case unreadable > 1:
		suffix = fmt.Sprintf(" (%d files unreadable)", unreadable)
	}

	total := sum.Admitted + sum.Refused
	if total == 0 {
		fmt.Fprintln(out, "ℹ "+prefix+"no assistant lines"+suffix)
		return
	}
	clause := fmt.Sprintf("%s%d of %d assistant lines refused", prefix, sum.Refused, total)
	rate := float64(sum.Refused) / float64(total)
	if sum.Refused >= refusalMinLines && rate >= refusalRateLimit {
		check(out, fmt.Sprintf("%s (%.1f%%)%s — Claude Code's transcript format may have changed and totals are under-counted; "+
			"parse-errors.log names the files. Upgrade ccpulse or file an issue.", clause, 100*rate, suffix), false, nil)
		return
	}
	fmt.Fprintln(out, "ℹ "+clause+suffix)
}

// scanAdmissionWindow sums parse.ScanAdmission over every *.jsonl under
// projectsRoot modified at or after cutoff, counting the files it could not
// read. Unreadable directories are skipped. The only error it returns is
// ctx's, when the walk was cancelled.
func scanAdmissionWindow(ctx context.Context, projectsRoot string, cutoff time.Time) (parse.Admission, int, error) {
	var sum parse.Admission
	unreadable := 0
	err := filepath.WalkDir(projectsRoot, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			switch {
			case d == nil: // the root itself could not be stat'ed
			case d.IsDir():
				return fs.SkipDir
			case strings.HasSuffix(d.Name(), ".jsonl"):
				unreadable++
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			unreadable++
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		a, serr := parse.ScanAdmission(path)
		if serr != nil {
			unreadable++
			return nil
		}
		sum.Admitted += a.Admitted
		sum.Refused += a.Refused
		return nil
	})
	return sum, unreadable, err
}
