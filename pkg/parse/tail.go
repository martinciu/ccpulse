package parse

// ParseFromOffset opens path, seeks to startOffset, and parses to EOF,
// returning new messages plus the post-parse byte offset and line number.
// Lines starting before startOffset are skipped entirely.
//
// It is a thin wrapper over ParseFromOffsetWithErrors that discards the
// per-line ParseError records: malformed lines and skipped oversized lines
// are swallowed silently, matching this variant's historical contract.
// Callers that need parse-error visibility use the WithErrors variant.
//
//nolint:revive // ParseFromOffset stutters; retained for compatibility — part of the established incremental-tail API
func ParseFromOffset(path, slug string, startOffset int64, startLine int) ([]Message, int64, int, error) {
	msgs, _, off, line, err := ParseFromOffsetWithErrors(path, slug, startOffset, startLine)
	return msgs, off, line, err
}
