package canonical

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// helper: init a git repo at path with one commit
func gitInit(t *testing.T, path string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init", "-q"},
		{"git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
}

func TestResolveGitRepo(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	// Resolve symlinks so the comparison works on macOS (/var → /private/var).
	wantRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveGit(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != wantRoot {
		t.Errorf("Root = %s, want %s", got.Root, wantRoot)
	}
	if got.Branch == "" {
		t.Error("Branch empty")
	}
	if got.IsWorktree {
		t.Error("expected IsWorktree=false")
	}
}
