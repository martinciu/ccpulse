package canonical

import (
	"os"
	"path/filepath"
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
