package canonical

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeSlug(t *testing.T) {
	// Lay out a fake filesystem under t.TempDir() so the walk has
	// something to verify.
	root := t.TempDir()
	must := func(p string) string {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(full, 0755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	must("Users/x/code/dotfiles")
	must("Users/x/code/dotfiles/.claude/worktrees/156-zsh-cleanup")

	cases := []struct {
		slug   string
		want   string
		wantOK bool
	}{
		// Existing positive cases.
		{"-Users-x-code-dotfiles", "/Users/x/code/dotfiles", true},
		{"-Users-x-code-dotfiles--claude-worktrees-156-zsh-cleanup",
			"/Users/x/code/dotfiles/.claude/worktrees/156-zsh-cleanup", true},
		// Traversal rejections — must return ("", false) without
		// touching the filesystem on the escape target.
		{"-Users-x-..-etc-passwd", "", false}, // middle .. (issue #47 example)
		{"-..-etc-passwd", "", false},         // leading ..
		{"-Users-x-code-..", "", false},       // trailing ..
		{"-..", "", false},                    // root-only ..
	}
	for _, c := range cases {
		got, ok := DecodeSlugIn(c.slug, root)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v (got=%q)", c.slug, ok, c.wantOK, got)
			continue
		}
		if !c.wantOK {
			if got != "" {
				t.Errorf("%s: rejected slug returned non-empty path %q", c.slug, got)
			}
			continue
		}
		want := root + c.want
		if got != want {
			t.Errorf("%s = %s, want %s", c.slug, got, want)
		}
	}
}

func FuzzDecodeSlugIn(f *testing.F) {
	// Seed corpus: traversal shapes, edge cases, and round-trip-able legit slugs.
	seeds := []string{
		// Traversal — must all return false.
		"-..-etc-passwd",
		"-Users-x-..-etc-passwd",
		"-x-..-y",
		"-Users-x-code-..",
		"-..",
		// Lookalikes that are NOT `..` and should be treated as ordinary segments.
		"-x-...-y",
		"-x-....-y",
		// Legitimate Claude-style slugs.
		"-Users-x-code-dotfiles",
		"-Users-x-code-dotfiles--claude-worktrees-156-zsh-cleanup",
		// Pathological shapes.
		"",
		"-",
		"--",
		"---",
		"-\x00",
		"-x\x00y",
		// Long input.
		"-" + strings.Repeat("a-", 600),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Use a single tmpdir as the fuzz root. Most fuzz inputs won't
	// resolve to anything real under the tmpdir; that's fine — we
	// only assert non-panic and the structural invariants below.
	root := f.TempDir()

	f.Fuzz(func(t *testing.T, slug string) {
		got, ok := DecodeSlugIn(slug, root)
		if !ok {
			if got != "" {
				t.Errorf("slug=%q: !ok but path=%q (want empty)", slug, got)
			}
			return
		}
		// Successful decodes must:
		//   1. live under the test root (or BE the test root), and
		//   2. contain no `..` segment.
		// The trailing-slash check on the prefix matters: it rejects a
		// sibling like `/tmpfoo` against root `/tmp` that plain HasPrefix
		// would accept.
		if got != root && !strings.HasPrefix(got, root+"/") {
			t.Errorf("slug=%q: returned path %q escapes root %q", slug, got, root)
		}
		rel := strings.TrimPrefix(got, root)
		for seg := range strings.SplitSeq(rel, "/") {
			if seg == ".." {
				t.Errorf("slug=%q: returned path %q has `..` segment", slug, got)
			}
		}
	})
}
