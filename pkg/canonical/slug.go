// Package canonical resolves Claude Code session slugs to filesystem
// paths and (later) to canonical project roots via git.
package canonical

import (
	"os"
	"slices"
	"strings"
)

// DecodeSlug walks candidate slug decodings against the real filesystem.
// Returns the longest path that os.Stat reports as existing.
//
// Encoding rules: '/' → '-' and '.' → '--'. The decode is ambiguous
// because '-' is valid in directory names, so we test candidates.
func DecodeSlug(slug string) (string, bool) {
	return DecodeSlugIn(slug, "/")
}

// DecodeSlugIn is DecodeSlug with a configurable root, useful for tests.
func DecodeSlugIn(slug, root string) (string, bool) {
	if !strings.HasPrefix(slug, "-") {
		return "", false
	}
	// Replace '--' → '/.' first (it captures the leading-dot pattern),
	// then '-' → '/'. This handles the common shape
	// `<repo>/.claude/worktrees/<branch>`.
	candidate := strings.TrimPrefix(slug, "-")
	candidate = strings.ReplaceAll(candidate, "--", "/.")
	candidate = strings.ReplaceAll(candidate, "-", "/")

	// Reject path-traversal slugs before any os.Stat. The slug
	// encoding (`/` → `-`, `.` → `--`) never produces `..` on its
	// own, so a `..` segment in the decoded candidate can only come
	// from a literal `..` in the slug directory name and is
	// unambiguously hostile or malformed input. The shrink-loop
	// fallback below rejoins these same segments, so checking once
	// here covers both code paths.
	if slices.Contains(strings.Split(candidate, "/"), "..") {
		return "", false
	}

	joinPath := func(r, p string) string {
		if r == "/" {
			return "/" + p
		}
		return r + "/" + p
	}
	full := joinPath(root, candidate)
	if _, err := os.Stat(full); err == nil {
		return full, true
	}
	// Fallback: keep collapsing '-' segments until something exists.
	parts := strings.Split(candidate, "/")
	for shrink := len(parts); shrink > 1; shrink-- {
		head := strings.Join(parts[:shrink-1], "/")
		tail := strings.Join(parts[shrink-1:], "-")
		full := joinPath(root, head+"/"+tail)
		if _, err := os.Stat(full); err == nil {
			return full, true
		}
	}
	return "", false
}
