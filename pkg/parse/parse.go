// Package parse turns Claude Code JSONL transcripts into Message records.
package parse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Message is a single assistant turn decoded from a Claude Code JSONL transcript line.
type Message struct {
	SessionID          string
	MessageID          string
	ProjectSlug        string
	Timestamp          time.Time
	Role               string
	Model              string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	IsSubagent         bool
	ParentSessionID    string
	Cwd                string
	GitBranch          string
	RepoRoot           string
	Effort             string
	IterationsJSON     string
}

type rawLine struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	Timestamp time.Time `json:"timestamp"`
	Cwd       string    `json:"cwd"`
	GitBranch string    `json:"gitBranch"`
	Effort    string    `json:"effort"`
	Message   struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            struct {
				Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			Iterations json.RawMessage `json:"iterations"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseError records a per-line parsing failure with its absolute line number and underlying error.
//
//nolint:revive // ParseError stutters; retained for compatibility — callers already reference it as parse.ParseError
type ParseError struct {
	Line int
	Err  error
}

// ErrOversizedLineSkipped is wrapped into the synthesised ParseError
// produced when a line exceeds ScannerMaxBytes. Callers can inspect
// the recovery class with errors.Is rather than substring-matching
// the formatted message. The wrapped chain also contains
// bufio.ErrTooLong so callers can disambiguate the underlying scanner
// failure if needed.
var ErrOversizedLineSkipped = errors.New("oversized line skipped")

// ErrZeroTimestamp is the ParseError cause reported when an assistant line
// carries no usable `timestamp`: the key is absent, or it decodes to a year-1
// instant. Go decodes a MISSING timestamp to the zero time.Time, which is not a
// point on any axis ccpulse draws — it is the absence of a timestamp, wearing a
// number. (An EMPTY string is refused one level earlier, inside time.Time's own
// UnmarshalJSON, so it arrives as a generic decode error rather than this
// sentinel. Both outcomes keep the line out of the cache; only this one is
// classifiable with errors.Is.)
//
// The test is Year() <= 1, not IsZero(): IsZero() compares against one exact
// instant, so "0001-01-01T00:00:00+01:00" and "0001-01-02T00:00:00Z" — equally
// unplaceable, and equally capable of stretching the chart across two millennia
// — would slip past it and be stored.
//
// Storing such a row is not a cosmetic wart: the chart spans earliest-message
// → now at EVERY zoom (#53), so one year-1 row stretches the canvas across two
// millennia. A single line of this shape put the 1h zoom at ~17.7M buckets and
// wedged the TUI before its first frame (#527). Skipping the line here keeps it
// out of the cache entirely, and reporting it as a ParseError means it lands in
// parse-errors.log instead of vanishing.
var ErrZeroTimestamp = errors.New("assistant line has no usable timestamp")

// ParseWithErrors parses every line and returns successfully-parsed
// messages plus per-line parse errors. On bufio.ErrTooLong the scanner
// is unrecoverable (no seek on io.Reader), so the oversized line is
// reported as a synthesised ParseError, the function returns nil
// error, and any lines after the oversized one are not yielded.
// Callers that need to skip past the oversized line and continue
// parsing must use the file-based ParseFromOffsetWithErrors.
//
//nolint:revive // ParseWithErrors stutters; retained for compatibility — callers that dot-import would lose all parse. prefix
func ParseWithErrors(r io.Reader, projectSlug string) ([]Message, []ParseError, error) {
	var msgs []Message
	var errs []ParseError
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, scannerInitialCap()), ScannerMaxBytes)
	line := 0
	for sc.Scan() {
		line++
		var raw rawLine
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			errs = append(errs, ParseError{Line: line, Err: err})
			continue
		}
		if raw.Type != "assistant" {
			continue
		}
		got, tsErr := assistantMessages(raw, projectSlug)
		if tsErr != nil {
			errs = append(errs, ParseError{Line: line, Err: tsErr})
			continue
		}
		msgs = append(msgs, got...)
	}
	err := sc.Err()
	if err != nil && errors.Is(err, bufio.ErrTooLong) {
		errs = append(errs, ParseError{
			Line: line + 1,
			Err:  fmt.Errorf("%w; cannot recover from io.Reader: %w", ErrOversizedLineSkipped, bufio.ErrTooLong),
		})
		return msgs, errs, nil
	}
	return msgs, errs, err
}

// assistantMessages returns the rows to store for a decoded assistant line, or
// a non-nil error naming why the line was skipped. Single source of truth for
// admission: both parse entry points (ParseWithErrors and
// ParseFromOffsetWithErrors) route through it, so a rule added here cannot be
// enforced by one path and missed by the other.
func assistantMessages(raw rawLine, slug string) ([]Message, error) {
	if raw.Timestamp.Year() <= 1 {
		return nil, ErrZeroTimestamp
	}
	return toMessages(raw, slug), nil
}

// toMessage converts a parsed JSONL line into a Message.
func toMessage(raw rawLine, slug string) Message {
	return Message{
		SessionID:          raw.SessionID,
		MessageID:          raw.Message.ID,
		ProjectSlug:        slug,
		Timestamp:          raw.Timestamp,
		Role:               raw.Message.Role,
		Model:              raw.Message.Model,
		InputTokens:        raw.Message.Usage.InputTokens,
		OutputTokens:       raw.Message.Usage.OutputTokens,
		CacheReadTokens:    raw.Message.Usage.CacheReadInputTokens,
		CacheWrite5mTokens: raw.Message.Usage.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1hTokens: raw.Message.Usage.CacheCreation.Ephemeral1hInputTokens,
		Cwd:                raw.Cwd,
		GitBranch:          raw.GitBranch,
		Effort:             raw.Effort,
	}
}

// iterationEntry is the typed probe informativeIterations uses to compare an
// iterations entry against the outer usage. Unknown fields are ignored here;
// whenever the blob is stored it is stored verbatim, never re-marshaled — but
// an entry that matches the outer usage on all known fields is dropped as
// redundant even if it carries unknown extra fields.
type iterationEntry struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Type  string `json:"type"`
	Model string `json:"model"`
}

// maxIterationsProbe bounds the decode probe in informativeIterations, not
// storage. Real iterations arrays are a handful of entries; ~3-byte "{}," entries
// decode to ~96-byte structs, so an unbounded json.Unmarshal on a hostile blob
// near the scanner's line cap can balloon to gigabytes transiently. Blobs over
// this size are stored verbatim without probing (fail open; storage stays
// bounded by the input size, not by this constant).
const maxIterationsProbe = 1 << 20 // 1 MiB

// attemptKeySep joins a real message.id with an iterations index to form an
// attempt row's message_id ("<id>:it:<idx>"). Reserved like the "synthetic:"
// prefix: real message.id values must not embed it.
const attemptKeySep = ":it:"

// expandIterations classifies message.usage.iterations. blob is what the
// parent stores ("" when absent or redundant — identical semantics to the
// previous informativeIterations). entries is non-nil only when the blob is
// informative AND decodable; nil means no expansion (absent, redundant,
// oversized, or fail-open verbatim).
func expandIterations(raw rawLine) (blob string, entries []iterationEntry) {
	its := raw.Message.Usage.Iterations
	if len(its) == 0 {
		return "", nil // key absent
	}
	if len(its) > maxIterationsProbe {
		return string(its), nil // too large to probe cheaply; keep verbatim (fail open)
	}
	var es []iterationEntry
	if err := json.Unmarshal(its, &es); err != nil {
		return string(its), nil // fail open: store verbatim, never expand
	}
	if len(es) == 0 {
		return "", nil // JSON null or empty array
	}
	if len(es) == 1 && !informativeEntry(es[0], raw) {
		return "", nil // restates the flat usage columns
	}
	return string(its), es
}

// informativeIterations returns the blob expandIterations would store on the
// parent; kept as a named wrapper because the oversized-probe test targets it.
func informativeIterations(raw rawLine) string {
	blob, _ := expandIterations(raw)
	return blob
}

// tokenSums accumulates the five stored token fields across iterations
// entries. The cache_creation_input_tokens total is deliberately unused —
// only the 5m/1h split is stored, exactly as outer usage is parsed.
type tokenSums struct{ in, out, cacheRead, cw5m, cw1h int64 }

func (s *tokenSums) add(e iterationEntry) {
	s.in += e.InputTokens
	s.out += e.OutputTokens
	s.cacheRead += e.CacheReadInputTokens
	s.cw5m += e.CacheCreation.Ephemeral5mInputTokens
	s.cw1h += e.CacheCreation.Ephemeral1hInputTokens
}

// zeroTokens reports whether an entry carries no stored token usage at all.
func zeroTokens(e iterationEntry) bool {
	return e.InputTokens == 0 && e.OutputTokens == 0 && e.CacheReadInputTokens == 0 &&
		e.CacheCreation.Ephemeral5mInputTokens == 0 && e.CacheCreation.Ephemeral1hInputTokens == 0
}

// toMessages converts a parsed JSONL line into one or more Messages: the
// parent turn, plus one attempt row per iterations entry billed to a model
// other than the serving one (refused fallback attempts, cross-model
// advisors — see issue #456). When iterations are informative, they are
// authoritative: the parent's tokens become the sum of the own-model
// entries, which equals the outer usage on every verified live blob and
// repairs the rare near-zeroed outer usage object.
func toMessages(raw rawLine, slug string) []Message {
	m := toMessage(raw, slug)
	blob, entries := expandIterations(raw)
	m.IterationsJSON = blob
	if entries == nil || raw.Message.ID == "" {
		// No expansion: blob not informative or not decodable, or the line
		// has no message.id (a bare ":it:<idx>" key would collide across
		// turns in a session; iterations imply CC >= 2.1.119, which always
		// writes ids, so this guard is theoretical).
		return []Message{m}
	}
	msgs := []Message{m}
	var own tokenSums
	for i, e := range entries {
		if e.Model == "" || e.Model == raw.Message.Model {
			own.add(e)
			continue
		}
		if zeroTokens(e) {
			continue
		}
		a := m
		a.MessageID = raw.Message.ID + attemptKeySep + strconv.Itoa(i)
		a.Model = e.Model
		a.InputTokens = e.InputTokens
		a.OutputTokens = e.OutputTokens
		a.CacheReadTokens = e.CacheReadInputTokens
		a.CacheWrite5mTokens = e.CacheCreation.Ephemeral5mInputTokens
		a.CacheWrite1hTokens = e.CacheCreation.Ephemeral1hInputTokens
		a.IterationsJSON = ""
		msgs = append(msgs, a)
	}
	if len(msgs) == 1 && own == (tokenSums{}) {
		// Degenerate blob: every entry was zero (own folded to nothing,
		// foreign entries skipped). Keep the outer usage — silently
		// zeroing real billed tokens is worse than trusting the flat
		// columns (fail open).
		return msgs
	}
	msgs[0].InputTokens = own.in
	msgs[0].OutputTokens = own.out
	msgs[0].CacheReadTokens = own.cacheRead
	msgs[0].CacheWrite5mTokens = own.cw5m
	msgs[0].CacheWrite1hTokens = own.cw1h
	return msgs
}

// informativeEntry reports whether a single iterations entry differs from the
// outer envelope in any stored dimension — entry type, model, or any token
// count. False means the entry merely restates the flat usage columns.
func informativeEntry(e iterationEntry, raw rawLine) bool {
	u := raw.Message.Usage
	return e.Type != "message" ||
		(e.Model != "" && e.Model != raw.Message.Model) ||
		e.InputTokens != u.InputTokens ||
		e.OutputTokens != u.OutputTokens ||
		e.CacheReadInputTokens != u.CacheReadInputTokens ||
		e.CacheCreationInputTokens != u.CacheCreationInputTokens ||
		e.CacheCreation.Ephemeral5mInputTokens != u.CacheCreation.Ephemeral5mInputTokens ||
		e.CacheCreation.Ephemeral1hInputTokens != u.CacheCreation.Ephemeral1hInputTokens
}

// Parse is a convenience wrapper around ParseWithErrors that drops
// per-line error details (callers that want them use ParseWithErrors).
func Parse(r io.Reader, projectSlug string) ([]Message, error) {
	msgs, _, err := ParseWithErrors(r, projectSlug)
	return msgs, err
}
