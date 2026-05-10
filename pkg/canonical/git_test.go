package canonical

import (
	"errors"
	"io/fs"
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

func TestHardenedArgs(t *testing.T) {
	got := hardenedArgs("/some/dir", "rev-parse", "--show-toplevel")
	want := []string{
		"-c", "protocol.allow=never",
		"-c", "core.fsmonitor=false",
		"-c", "core.hookspath=/dev/null",
		"-C", "/some/dir",
		"rev-parse", "--show-toplevel",
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got = %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestGitHardeningBlocksExtTransport verifies that protocol.allow=never
// passed via -c blocks the ext:: external transport, even when a remote
// with a malicious ext:: URL is planted in .git/config. Without the
// hardening, ext:: would spawn the attacker's shell command (touching
// a sentinel file) before failing. With the hardening, git refuses
// the transport and the sentinel never appears.
//
// This is the regression test for the in-place planted-.git/config
// exploit class (#45). We use git fetch (the operation that consumes
// remote URLs) called via the package-private git() helper directly,
// because ResolveGit doesn't run fetch.
func TestGitHardeningBlocksExtTransport(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	sentinel := filepath.Join(tmp, "pwn.sentinel")

	// Plant a remote whose URL invokes ext:: with a shell command
	// that touches the sentinel. Without protocol.allow=never, git
	// fetch would spawn this and create the file.
	cfgPath := filepath.Join(repo, ".git", "config")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	extURL := `ext::sh -c "touch ` + sentinel + `"`
	if _, err := f.WriteString("[remote \"evil\"]\n\turl = " + extURL + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Fetch is expected to fail (protocol.allow=never refuses every
	// transport). What matters is that the failure happens BEFORE
	// the ext:: shell command runs.
	_, _ = git(repo, "fetch", "evil")

	if _, err := os.Stat(sentinel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("sentinel %s exists: protocol.allow=never did not block ext::", sentinel)
	}
}
