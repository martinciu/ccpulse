package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/secfile"
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
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(contents), 0o600); err != nil {
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
		name  string
		input string
		want  bool
	}{
		{
			name:  "empty object",
			input: `{}`,
			want:  false,
		},
		{
			name:  "hooks present but Stop absent",
			input: `{"hooks": {"PreToolUse": []}}`,
			want:  false,
		},
		{
			name:  "canonical matching shape",
			input: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"ccpulse status"}]}]}}`,
			want:  true,
		},
		// Loose substring match is deliberate — wrappers/aliases like
		// "my-ccpulse-wrapper" should still count as a ccpulse Stop hook.
		{
			name:  "command contains ccpulse as substring",
			input: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my-ccpulse-wrapper"}]}]}}`,
			want:  true,
		},
		{
			name:  "command is an array, not a string",
			input: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":["ccpulse"]}]}]}}`,
			want:  false,
		},
		{
			name:  "top-level value is an array",
			input: `[]`,
			want:  false,
		},
		{
			name:  "nil byte slice",
			input: "",
			want:  false,
		},
		{
			name:  "Stop key present but empty array",
			input: `{"hooks":{"Stop":[]}}`,
			want:  false,
		},
		{
			name:  "command is JSON null",
			input: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":null}]}]}}`,
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hookCommandMentionsCcpulse([]byte(tc.input))
			if got != tc.want {
				t.Errorf("input: %q\ngot %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestDoctor_SurfacesLockHeld confirms that when another fd holds
// LOCK_EX on the cache lock file, `ccpulse doctor` surfaces the
// wrapped ErrLockHeld message on the existing "cache db opens" line.
//
// Per-fd flock semantics: two fds in the same process are treated
// independently, so this in-process holder reliably blocks
// cache.Open's LOCK_SH acquire.
func TestDoctor_SurfacesLockHeld(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", dir)
	t.Setenv("CCPULSE_PROJECTS_ROOT", dir) // dummy; doctor only stats it

	dbPath := filepath.Join(dir, "state.db")
	lockPath := dbPath + ".lock"

	// Pre-create the lock file at the same perms acquireCacheLock
	// would; then take LOCK_EX on it from a fixture fd.
	holder, err := secfile.OpenFile(lockPath, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatalf("open holder lock file: %v", err)
	}
	t.Cleanup(func() { holder.Close() })
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock LOCK_EX on holder: %v", err)
	}

	// Run doctor with stdout captured.
	var buf bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor execute: %v", err)
	}
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("cache db opens")) {
		t.Fatalf("doctor output missing 'cache db opens' line:\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte(cache.ErrLockHeld.Error())) {
		t.Fatalf("doctor output missing ErrLockHeld message:\n%s", out)
	}
}
