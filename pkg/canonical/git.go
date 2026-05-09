package canonical

import (
	"errors"
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

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
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
