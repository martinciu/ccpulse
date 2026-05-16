package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSettings creates a temp HOME with ~/.claude/settings.json containing
// the given contents and returns the temp home dir. If contents is empty,
// no settings.json is written (file-missing case).
func writeSettings(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if contents == "" {
		return home
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(contents), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return home
}

func TestCheckClaudeCodeHook_FileMissing(t *testing.T) {
	writeSettings(t, "")

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)

	got := buf.String()
	if !strings.Contains(got, "ℹ Claude Code settings.json not found") {
		t.Errorf("expected info line for missing settings.json, got: %q", got)
	}
}

func TestCheckClaudeCodeHook_MatchingHookPresent(t *testing.T) {
	settings := `{
		"hooks": {
			"Stop": [
				{
					"matcher": "",
					"hooks": [
						{"type": "command", "command": "ccpulse status --quiet"}
					]
				}
			]
		}
	}`
	writeSettings(t, settings)

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)
	got := buf.String()
	if !strings.Contains(got, "✓ ccpulse Stop hook detected") {
		t.Errorf("expected ✓ line, got: %q", got)
	}
}

func TestCheckClaudeCodeHook_NoMatchingHook(t *testing.T) {
	// Stop hook exists but runs something unrelated.
	settings := `{
		"hooks": {
			"Stop": [
				{"matcher": "", "hooks": [{"type": "command", "command": "echo done"}]}
			]
		}
	}`
	writeSettings(t, settings)

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)
	got := buf.String()
	if !strings.Contains(got, "✗ no ccpulse Stop hook") {
		t.Errorf("expected ✗ line, got: %q", got)
	}
	if !strings.Contains(got, "ccpulse status --quiet") {
		t.Errorf("expected snippet to be printed, got: %q", got)
	}
}

func TestREADMEContainsHookSnippet(t *testing.T) {
	// Walk up from this test's working dir to the repo root so the test
	// works from worktrees and the top-level checkout alike.
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !strings.Contains(string(data), claudeStopHookSnippet) {
		t.Errorf("README.md must contain the claudeStopHookSnippet verbatim — doctor and README drifted apart.\n"+
			"snippet:\n%s\n\nREADME excerpt around 'Claude Code':\n%s",
			claudeStopHookSnippet, excerptAround(string(data), "Claude Code", 200))
	}
}

func excerptAround(s, needle string, radius int) string {
	i := strings.Index(s, needle)
	if i < 0 {
		return "(needle not found)"
	}
	start := max(0, i-radius)
	end := min(len(s), i+radius)
	return s[start:end]
}

func TestCheckClaudeCodeHook_MalformedJSON(t *testing.T) {
	writeSettings(t, "{not json")

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)
	got := buf.String()
	// Per spec: malformed user config should not fail the health check.
	// Parse failure intentionally surfaces as ✗ no-match (nudging the user
	// to fix their settings.json), and contents must never be echoed back.
	if !strings.Contains(got, "✗ no ccpulse Stop hook") {
		t.Errorf("malformed JSON should produce ✗ no-hook line (parse failure treated as no-match), got: %q", got)
	}
	if strings.Contains(got, "not json") {
		t.Errorf("must not echo settings.json contents, got: %q", got)
	}
}

func TestHookCommandMentionsCcpulse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "empty object",
			in:   `{}`,
			want: false,
		},
		{
			name: "hooks present but Stop absent",
			in:   `{"hooks": {"PreToolUse": []}}`,
			want: false,
		},
		{
			name: "canonical matching shape",
			in:   `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"ccpulse status"}]}]}}`,
			want: true,
		},
		{
			name: "command contains ccpulse as substring",
			in:   `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my-ccpulse-wrapper"}]}]}}`,
			want: true,
		},
		{
			name: "command is an array, not a string",
			in:   `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":["ccpulse"]}]}]}}`,
			want: false,
		},
		{
			name: "top-level value is an array",
			in:   `[]`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hookCommandMentionsCcpulse([]byte(tc.in))
			if got != tc.want {
				t.Errorf("input: %s\ngot %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
