// Package parse turns Claude Code JSONL transcripts into Message records.
package parse

import (
	"bufio"
	"encoding/json"
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

func Parse(r io.Reader, projectSlug string) ([]Message, error) {
	var msgs []Message
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // up to 16 MB per line
	for sc.Scan() {
		var raw rawLine
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue // skip malformed; full skip-and-log behavior added in Task 7
		}
		if raw.Type != "assistant" {
			continue
		}
		msgs = append(msgs, Message{
			SessionID:          raw.SessionID,
			ProjectSlug:        projectSlug,
			Timestamp:          raw.Timestamp,
			Role:               raw.Message.Role,
			Model:              raw.Message.Model,
			InputTokens:        raw.Message.Usage.InputTokens,
			OutputTokens:       raw.Message.Usage.OutputTokens,
			CacheReadTokens:    raw.Message.Usage.CacheReadInputTokens,
			CacheWrite5mTokens: raw.Message.Usage.CacheCreation.Ephemeral5mInputTokens,
			CacheWrite1hTokens: raw.Message.Usage.CacheCreation.Ephemeral1hInputTokens,
		})
	}
	return msgs, sc.Err()
}
