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

// TestScaffold_ParsesCleanly guards against future default.toml edits
// (multi-line strings, inline tables, etc.) silently breaking the
// line-based commenting transform in Scaffold.
func TestScaffold_ParsesCleanly(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(string(Scaffold()), &cfg); err != nil {
		t.Fatalf("Scaffold output failed to parse: %v", err)
	}
}

// TestScaffold_EqualsNoConfig is the actual "all defaults take effect"
// invariant — a freshly-created config file loads identically to having
// no config file at all.
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

// TestScaffold_UncommentEachKey verifies that uncommenting any single
// supported key in the scaffold produces the expected override. One
// row per key; covers every field on Config.
func TestScaffold_UncommentEachKey(t *testing.T) {
	tests := []struct {
		key      string
		override string // replacement line (no leading "# ")
		want     Config
	}{
		{
			key:      "retention_days",
			override: "retention_days = 7",
			want: Config{
				History: History{RetentionDays: 7},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:      "projects_root",
			override: `projects_root = "/custom"`,
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "/custom", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:      "cache_dir",
			override: `cache_dir = "/explicit"`,
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: "/explicit"},
				UI:      UI{ReduceMotion: false},
			},
		},
		{
			key:      "reduce_motion",
			override: "reduce_motion = true",
			want: Config{
				History: History{RetentionDays: 0},
				Paths:   Paths{ProjectsRoot: "~/.claude/projects", CacheDir: defaultCacheDir()},
				UI:      UI{ReduceMotion: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			// Replace the commented "# <key> = <default>" line with the
			// uncommented override. Match leading "# " and the key name
			// to avoid touching unrelated lines.
			scaffold := Scaffold()
			re := regexp.MustCompile(`(?m)^# ` + regexp.QuoteMeta(tt.key) + ` =.*$`)
			modified := re.ReplaceAllLiteral(scaffold, []byte(tt.override))
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

// Sanity check on the algorithm: the scaffold contains the expected
// active section headers and commented keys. Spot-check, not a golden.
func TestScaffold_ShapeSpotCheck(t *testing.T) {
	got := string(Scaffold())
	wantSubstrings := []string{
		"# ccpulse config — managed by you, never overwritten.",
		`# See "ccpulse config show" for the live values`,
		"\n[history]\n",
		"\n[paths]\n",
		"\n[ui]\n",
		"\n# retention_days = 0\n",
		`# projects_root = "~/.claude/projects"`,
		`# cache_dir = ""`,
		"# reduce_motion = false",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("Scaffold() missing expected substring %q\nfull output:\n%s", sub, got)
		}
	}
	// No double blank lines.
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("Scaffold() contains consecutive blank lines (dedupe regression):\n%s", got)
	}
}
