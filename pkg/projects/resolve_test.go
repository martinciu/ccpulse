package projects

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	tmp := t.TempDir()

	// A plain repo: <tmp>/repo/.git is a directory.
	repo := filepath.Join(tmp, "repo")
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustMkdir(t, filepath.Join(repo, "pkg", "tui"))

	// A linked worktree: <tmp>/repo/.claude/worktrees/feat/.git is a FILE
	// pointing at <repo>/.git/worktrees/feat (worktrunk layout).
	wt := filepath.Join(repo, ".claude", "worktrees", "feat")
	mustMkdir(t, wt)
	mustWrite(t, filepath.Join(wt, ".git"),
		"gitdir: "+filepath.Join(repo, ".git", "worktrees", "feat")+"\n")

	r := New()
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"repo root", repo, repo},
		{"subdir of repo", filepath.Join(repo, "pkg", "tui"), repo},
		{"linked worktree folds to main", wt, repo},
		{"no git anywhere", tmp, ""},
		{"empty cwd", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Resolve(tt.cwd); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestHeuristicFallback(t *testing.T) {
	tests := []struct {
		name, cwd, want string
	}{
		{"claude worktree marker", "/u/code/ccpulse/.claude/worktrees/408-foo", "/u/code/ccpulse"},
		{"dot-worktrees marker", "/u/code/strava/.worktrees/feature/inngest-queue", "/u/code/strava"},
		{"no marker returns path", "/u/code/ccpulse/pkg/tui", "/u/code/ccpulse/pkg/tui"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HeuristicFallback(tt.cwd); got != tt.want {
				t.Errorf("HeuristicFallback(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestLabelFromRoot(t *testing.T) {
	if got := LabelFromRoot("/u/code/ccpulse"); got != "ccpulse" {
		t.Errorf("LabelFromRoot = %q, want ccpulse", got)
	}
	if got := LabelFromRoot(""); got != "(no project)" {
		t.Errorf("LabelFromRoot(\"\") = %q, want (no project)", got)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
