package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestScaffold_ParsesCleanly guards against the toml encoder ever producing
// output that won't round-trip through toml.Decode (e.g. if a future Config
// field grows a type the encoder can serialise but the decoder rejects).
func TestScaffold_ParsesCleanly(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(string(Scaffold()), &cfg); err != nil {
		t.Fatalf("Scaffold output failed to parse: %v", err)
	}
}

// TestScaffold_EqualsNoConfig pins the invariant that the bytes written
// on first run reload identically to having no config file at all. With
// the active-keys scaffold this is tautological for the current channel
// (the scaffold encodes Load("") and Load(scaffold) decodes back through
// the same path) — the test exists to catch a future regression where
// Scaffold and Load drift apart.
func TestScaffold_EqualsNoConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, Scaffold(), 0o600); err != nil {
		t.Fatalf("write scaffold: %v", err)
	}

	fromScaffold, err := Load(path)
	if err != nil {
		t.Fatalf("Load(scaffold): %v", err)
	}
	fromEmpty, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}

	if !reflect.DeepEqual(fromScaffold, fromEmpty) {
		t.Errorf("Load(scaffold) = %+v, want %+v", fromScaffold, fromEmpty)
	}
}

// TestScaffold_EditEachKey verifies that editing any single value in the
// scaffold produces the expected override on reload. One row per Config
// field; the keys ship active so this just rewrites their value in place.
func TestScaffold_EditEachKey(t *testing.T) {
	tests := []struct {
		key     string
		replace string // full replacement line (active, no leading '#')
		want    Config
	}{
		{
			key:     "retention_days",
			replace: "  retention_days = 7",
			want: Config{
				History: History{RetentionDays: 7},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:     "projects_root",
			replace: `  projects_root = "/custom"`,
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "/custom", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:     "cache_dir",
			replace: `  cache_dir = "/explicit"`,
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: "/explicit"},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:     "reduce_motion",
			replace: "  reduce_motion = true",
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			scaffold := Scaffold()
			re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(tt.key) + ` =.*$`)
			modified := re.ReplaceAllLiteral(scaffold, []byte(tt.replace))
			if bytes.Equal(modified, scaffold) {
				t.Fatalf("regexp did not match any line for key %q", tt.key)
			}

			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, modified, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestScaffold_ShapeSpotCheck pins the visible shape: header line present,
// all three section headers active, every key active (no leading '#'),
// and cache_dir baked to the channel-resolved path rather than left as
// an empty string.
func TestScaffold_ShapeSpotCheck(t *testing.T) {
	got := string(Scaffold())
	wantSubstrings := []string{
		"# ccpulse config — managed by you, never overwritten.",
		"\n[history]\n",
		"\n[paths]\n",
		"\n[ui]\n",
		"retention_days = 0",
		`projects_root = "~/.claude/projects"`,
		"reduce_motion = false",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("Scaffold() missing expected substring %q\nfull output:\n%s", sub, got)
		}
	}
	for _, banned := range []string{
		"# retention_days",
		"# projects_root",
		"# cache_dir",
		"# reduce_motion",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("Scaffold() contains commented key %q — keys must ship active\nfull output:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "cache_dir = ") {
		t.Errorf("Scaffold() missing cache_dir line\nfull output:\n%s", got)
	}
	if strings.Contains(got, `cache_dir = ""`) {
		t.Errorf("Scaffold() emitted empty cache_dir — should be baked to the channel-resolved path\nfull output:\n%s", got)
	}
}
