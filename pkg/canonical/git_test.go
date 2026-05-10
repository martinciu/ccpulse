package canonical

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestHardenedEnv(t *testing.T) {
	want := "GIT_OPTIONAL_LOCKS=0"
	got := hardenedEnv()
	if !slices.Contains(got, want) {
		t.Errorf("hardenedEnv() missing %q (len=%d)", want, len(got))
	}
}

// plantExtRemote initialises a fresh git repo and writes an `ext::sh`
// remote URL into its .git/config that touches a sentinel file when
// invoked. Returns the repo path and sentinel path. Used by both the
// hardening regression test and its negative control.
func plantExtRemote(t *testing.T) (repo, sentinel string) {
	t.Helper()
	tmp := t.TempDir()
	repo = filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	sentinel = filepath.Join(tmp, "pwn.sentinel")
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
	return repo, sentinel
}

// extTransportExploitable runs an unhardened `git fetch evil` against
// a planted ext:: remote and reports whether the sentinel was created.
// Used as a negative control for TestGitHardeningBlocksExtTransport so
// that test can skip itself if the exploit isn't reproducible on this
// git version (avoiding a tautological assertion).
func extTransportExploitable(t *testing.T) bool {
	t.Helper()
	repo, sentinel := plantExtRemote(t)
	cmd := exec.Command("git", "-C", repo, "fetch", "evil")
	_ = cmd.Run()
	_, err := os.Stat(sentinel)
	return !errors.Is(err, fs.ErrNotExist)
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
//
// The test self-validates by first running extTransportExploitable —
// if the unhardened invocation can't reproduce the exploit on this git
// version, the assertion below would be tautological, so we skip.
func TestGitHardeningBlocksExtTransport(t *testing.T) {
	if !extTransportExploitable(t) {
		t.Skip("ext:: transport not exploitable on this git — assertion would be tautological")
	}

	repo, sentinel := plantExtRemote(t)

	// Fetch is expected to fail (protocol.allow=never refuses every
	// transport). What matters is that the failure happens BEFORE
	// the ext:: shell command runs.
	_, _ = git(repo, "fetch", "evil")

	if _, err := os.Stat(sentinel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("sentinel %s exists: protocol.allow=never did not block ext::", sentinel)
	}
}

// TestGitEmptyArgs confirms git() doesn't panic when called with no
// subcommand args. All current callers pass a subcommand, but the
// implicit invariant gets a defensive guard so a future refactor
// won't crash on a one-liner that looks infallible.
func TestGitEmptyArgs(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("git() panicked on empty args: %v", r)
		}
	}()
	_, err := git("/nonexistent")
	if err == nil {
		t.Error("git() with no args returned nil err")
	}
}
