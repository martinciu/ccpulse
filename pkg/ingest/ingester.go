package ingest

import (
	"context"
	"errors"
	"os"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

var errTruncated = errors.New("recorded offset past EOF; resetting and re-parsing")

// Ingester catches one .jsonl transcript up to current EOF.
// Same code path runs from the watcher callback (one file per
// fsnotify event) and from the startup Backfill (every .jsonl).
type Ingester struct {
	Cache          *cache.Cache
	Pricing        pricing.History
	ProjectsRoot   string
	ParseErrorsLog string
}

// ProcessFile catches one .jsonl up to current EOF.
// Returns the count of newly inserted messages and a non-nil error
// only when the atomic message+cursor write itself fails. Stat /
// GetFile / parse failures are logged to ParseErrorsLog and the file
// is skipped (0, nil) so the backfill loop never aborts on a single
// bad file. The write commits rows and the file cursor in one
// transaction, so on a write error nothing landed and the count is 0.
func (i *Ingester) ProcessFile(ctx context.Context, path string) (inserted int, err error) {
	st, err := os.Stat(path)
	if err != nil {
		LogFileError(i.ParseErrorsLog, path, err)
		return 0, nil
	}

	// mtime stays discarded (unused here); only the error is newly captured.
	// := assigns to the named err return (same scope as the os.Stat line
	// above), so found/offset/line are the only newly declared names.
	_, offset, line, found, err := i.Cache.GetFile(ctx, path)
	if err != nil {
		// Real driver error (sql.ErrNoRows → err==nil, found==false, handled
		// below). Surface it and skip this pass — re-parsing a large file into
		// a broken DB just amplifies I/O. The next watcher event / backfill
		// retries, preserving the file's last good cursor.
		LogFileError(i.ParseErrorsLog, path, err)
		return 0, nil
	}

	// Skip optimisation: file recorded and size unchanged → nothing new.
	if found && offset == st.Size() {
		return 0, nil
	}

	// Truncation guard: file shrank since last record (manual edit,
	// rotation, etc.). Seek past EOF would yield zero rows and the
	// gap would persist forever — reset to the start instead.
	if found && offset > st.Size() {
		LogFileError(i.ParseErrorsLog, path, errTruncated)
		offset = 0
		line = 0
	}

	slug, isSub, parentSID := SlugAndSubagent(i.ProjectsRoot, path)

	msgs, perrs, newOff, newLine, parseErr := parse.ParseFromOffsetWithErrors(
		path, slug, offset, int(line),
	)
	if len(perrs) > 0 {
		AppendParseErrors(i.ParseErrorsLog, path, perrs)
	}
	if parseErr != nil {
		LogFileError(i.ParseErrorsLog, path, parseErr)
		return 0, nil
	}

	for k := range msgs {
		msgs[k].IsSubagent = isSub
		msgs[k].ParentSessionID = parentSID
	}

	// Rows and cursor commit together: either both land or neither does,
	// eliminating the re-parse window if the process dies mid-write. Called
	// regardless of len(msgs) so a content-free tail still advances the cursor
	// transactionally. On failure the tx rolled back, so zero rows landed.
	if err := i.Cache.InsertMessagesAndRecordFile(
		ctx, msgs, i.Pricing, path, st.ModTime().UnixNano(), newOff, int64(newLine),
	); err != nil {
		LogFileError(i.ParseErrorsLog, path, err)
		return 0, err
	}
	return len(msgs), nil
}
