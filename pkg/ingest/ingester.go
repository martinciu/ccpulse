package ingest

import (
	"os"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// Ingester catches one .jsonl transcript up to current EOF.
// Same code path runs from the watcher callback (one file per
// fsnotify event) and from the startup Backfill (every .jsonl).
type Ingester struct {
	Cache          *cache.Cache
	Resolver       *canonical.Resolver
	Pricing        pricing.Table
	ProjectsRoot   string
	ParseErrorsLog string
}

// ProcessFile catches one .jsonl up to current EOF.
// Returns the count of newly inserted messages and a non-nil error
// only when InsertMessages itself fails. Stat / open / parse /
// RecordFile failures are logged to ParseErrorsLog and swallowed
// so the backfill loop never aborts on a single bad file.
func (i *Ingester) ProcessFile(path string) (inserted int, err error) {
	st, err := os.Stat(path)
	if err != nil {
		LogFileError(i.ParseErrorsLog, path, err)
		return 0, nil
	}

	_, offset, line, found, _ := i.Cache.GetFile(path)

	// Skip optimisation: file recorded and size unchanged → nothing new.
	if found && offset == st.Size() {
		return 0, nil
	}

	slug, _, _ := SlugAndSubagent(i.ProjectsRoot, path)

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

	if len(msgs) > 0 {
		if err := i.Cache.InsertMessages(msgs, i.Pricing); err != nil {
			LogFileError(i.ParseErrorsLog, path, err)
			return 0, err
		}
	}

	if err := i.Cache.RecordFile(path, st.ModTime().UnixNano(), newOff, int64(newLine)); err != nil {
		LogFileError(i.ParseErrorsLog, path, err)
		// Do not return the error: idempotent re-parse will recover
		// next time. Caller treats this file as successfully ingested.
	}
	return len(msgs), nil
}
