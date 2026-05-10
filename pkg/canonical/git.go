package canonical

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type GitInfo struct {
	Root       string // worktree root
	CommonDir  string // .git common-dir (main repo's .git)
	Branch     string
	IsWorktree bool
}

// hardenedArgs returns the full git argv (everything after "git") for
// an invocation that disables every code-execution surface git has:
//
//   - protocol.allow=never blocks fetch/clone protocols and submodule
//     URLs that would resolve via git's transport machinery.
//   - core.fsmonitor=false overrides any planted [core] fsmonitor
//     directive — the most direct RCE surface for read-ish operations.
//   - core.hookspath=/dev/null redirects all hook lookups to an empty
//     directory, neutralising every hook-based execution path.
//
// The `-c` flags must precede `-C` per git's flag-ordering rules
// (global options before subcommand selectors).
//
// Exposed (package-private) so tests can verify the hardening surface
// without forking a code path or stubbing exec.Command.
func hardenedArgs(dir string, args ...string) []string {
	hardened := []string{
		"-c", "protocol.allow=never",
		"-c", "core.fsmonitor=false",
		"-c", "core.hookspath=/dev/null",
		"-C", dir,
	}
	return append(hardened, args...)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", hardenedArgs(dir, args...)...)
	// GIT_OPTIONAL_LOCKS=0 suppresses lockfile churn on the read-only
	// ops we run. Defence-in-depth: also keeps planted lock files from
	// being relevant.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ResolveGit(path string) (GitInfo, error) {
	gitDir, err := git(path, "rev-parse", "--git-dir")
	if err != nil {
		return GitInfo{}, err
	}
	commonDir, err := git(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return GitInfo{}, err
	}
	root, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitInfo{}, err
	}
	branch, _ := git(path, "branch", "--show-current") // may be empty (detached)

	// Resolve relative paths to absolute (git -C may print relative)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}

	abs := func(p string) string {
		r, _ := filepath.Abs(p)
		if resolved, err := filepath.EvalSymlinks(r); err == nil {
			return resolved
		}
		return r
	}
	gi := GitInfo{
		Root:       abs(root),
		CommonDir:  abs(commonDir),
		Branch:     branch,
		IsWorktree: abs(gitDir) != abs(commonDir),
	}
	if gi.Root == "" {
		return GitInfo{}, errors.New("empty toplevel")
	}
	return gi, nil
}
