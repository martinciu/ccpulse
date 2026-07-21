// Package parse turns Claude Code JSONL transcripts into Message records.
package parse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		msgs = append(msgs, toMessage(raw, projectSlug))
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
		IterationsJSON:     informativeIterations(raw),
	}
}

// iterationEntry is the typed probe informativeIterations uses to compare an
// iterations entry against the outer usage. Unknown fields are ignored here
// but preserved on disk — the raw bytes are stored verbatim, never re-marshaled.
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

// informativeIterations returns message.usage.iterations verbatim when the
// array carries information the flat usage columns do not: more than one
// attempt, a non-"message" entry type, an explicit model differing from the
// outer message.model, or token counts differing from the outer usage.
// Redundant single-attempt arrays (~99.9% of live data) return "" so the
// cache stores NULL instead of duplicating the flat columns. Content that
// fails to decode as []iterationEntry is kept verbatim (fail open): storing
// an odd blob beats silently dropping attempt data.
func informativeIterations(raw rawLine) string {
	its := raw.Message.Usage.Iterations
	if len(its) == 0 {
		return "" // key absent
	}
	var entries []iterationEntry
	if err := json.Unmarshal(its, &entries); err != nil {
		return string(its)
	}
	if len(entries) == 0 {
		return "" // JSON null or empty array
	}
	if len(entries) > 1 {
		return string(its)
	}
	if informativeEntry(entries[0], raw) {
		return string(its)
	}
	return ""
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
