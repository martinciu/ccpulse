package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckClaudeCodeHook_FileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)

	got := buf.String()
	if !strings.Contains(got, "ℹ Claude Code settings.json not found") {
		t.Errorf("expected info line for missing settings.json, got: %q", got)
	}
}

func TestCheckClaudeCodeHook_MatchingHookPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)
	got := buf.String()
	if !strings.Contains(got, "✓ ccpulse Stop hook detected") {
		t.Errorf("expected ✓ line, got: %q", got)
	}
}

func TestCheckClaudeCodeHook_NoMatchingHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Stop hook exists but runs something unrelated.
	settings := `{
		"hooks": {
			"Stop": [
				{"matcher": "", "hooks": [{"type": "command", "command": "echo done"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

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
	start := i - radius
	if start < 0 {
		start = 0
	}
	end := i + radius
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

func TestCheckClaudeCodeHook_MalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("{not json"), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	var buf bytes.Buffer
	checkClaudeCodeHook(&buf)
	got := buf.String()
	// Per spec: malformed user config should not fail the health check.
	// The output line should be informational, not ✗, and should not echo
	// the file contents back.
	if !strings.Contains(got, "✗ no ccpulse Stop hook") {
		t.Errorf("malformed JSON should produce ✗ no-hook line (parse failure treated as no-match), got: %q", got)
	}
	if strings.Contains(got, "not json") {
		t.Errorf("must not echo settings.json contents, got: %q", got)
	}
}
