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

type Message struct {
	SessionID          string
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
}

type rawLine struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
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
		} `json:"usage"`
	} `json:"message"`
}

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
		ProjectSlug:        slug,
		Timestamp:          raw.Timestamp,
		Role:               raw.Message.Role,
		Model:              raw.Message.Model,
		InputTokens:        raw.Message.Usage.InputTokens,
		OutputTokens:       raw.Message.Usage.OutputTokens,
		CacheReadTokens:    raw.Message.Usage.CacheReadInputTokens,
		CacheWrite5mTokens: raw.Message.Usage.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1hTokens: raw.Message.Usage.CacheCreation.Ephemeral1hInputTokens,
	}
}

// Parse is a convenience wrapper around ParseWithErrors that drops
// per-line error details (callers that want them use ParseWithErrors).
func Parse(r io.Reader, projectSlug string) ([]Message, error) {
	msgs, _, err := ParseWithErrors(r, projectSlug)
	return msgs, err
}
