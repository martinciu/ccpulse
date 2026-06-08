// Package projects resolves a Claude Code working directory (cwd) to a
// stable repo-root path so a repo's main checkout, its worktrees, and its
// subdirectories all collapse to a single project. Filesystem only — it
// never shells out to git.
package projects

import (
	"os"
	"path/filepath"
	"strings"
)

// worktreeMarkers are the path segments that introduce a worktree branch
// dir. Longest/most-specific first. Everything from the marker onward
// (including branch names, which can contain "/") is stripped by the
// string heuristic.
var worktreeMarkers = []string{"/.claude/worktrees/", "/.worktrees/"}

// gitWorktreePointer is the segment a linked worktree's .git FILE points
// through: "<main>/.git/worktrees/<name>". Git writes forward slashes.
const gitWorktreePointer = "/.git/worktrees/"

// Resolver resolves cwd → repo root. Stateless in v1 (safe for concurrent
// use by the startup cold-walk goroutine and the watcher callback). The
// struct is reserved for future config (e.g. prefix maps).
type Resolver struct{}

// New returns a ready Resolver.
func New() *Resolver { return &Resolver{} }

// Resolve walks cwd up to the filesystem root looking for a .git entry.
// Returns the repo root, or "" if none is found (or cwd is empty).
// Worktree-aware: a linked worktree's .git is a FILE containing
// "gitdir: <main>/.git/worktrees/<name>"; Resolve returns <main> so the
// main checkout and all its worktrees collapse to one root. Any stat/read
// error along the way is treated as "not a repo here" and the walk
// continues — Resolve never errors and never panics.
func (r *Resolver) Resolve(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := cwd
	for {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			if info.IsDir() {
				return dir
			}
			// .git is a file → linked worktree (or submodule).
			if main := mainRootFromGitFile(filepath.Join(dir, ".git")); main != "" {
				return main
			}
			return dir // unparseable pointer: treat this dir as its own root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root with no .git
		}
		dir = parent
	}
}

// mainRootFromGitFile reads a worktree .git file ("gitdir: <path>") and
// returns the MAIN repo root — the prefix before "/.git/worktrees/".
// Returns "" on any read/parse miss.
func mainRootFromGitFile(gitFile string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdir == line { // no "gitdir:" prefix was present
		return ""
	}
	if before, _, found := strings.Cut(gitdir, gitWorktreePointer); found {
		return before
	}
	return ""
}

// LabelFromRoot returns the display label for a repo root (its final path
// segment), or "(no project)" when root is empty.
func LabelFromRoot(root string) string {
	if root == "" {
		return "(no project)"
	}
	return filepath.Base(root)
}

// HeuristicFallback derives a best-effort repo-root PATH from the cwd
// STRING alone, used when Resolve fails because the path no longer exists
// on disk (e.g. a worktree removed by `wt remove`, whose transcript under
// ~/.claude/projects still references it). It strips a known worktree
// marker and everything after it and returns the remaining PATH — NOT the
// last segment — so a stale worktree row yields the SAME repo_root as the
// live main checkout (which Resolve returns as a full path) and the two
// GROUP together. A path with no marker returns itself (best-effort).
func HeuristicFallback(cwd string) string {
	if cwd == "" {
		return ""
	}
	for _, mk := range worktreeMarkers {
		if before, _, found := strings.Cut(cwd, mk); found {
			return strings.TrimRight(before, "/")
		}
	}
	return strings.TrimRight(cwd, "/")
}
