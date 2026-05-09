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
		slug string
		want string
	}{
		{"-Users-x-code-dotfiles", "/Users/x/code/dotfiles"},
		{"-Users-x-code-dotfiles--claude-worktrees-156-zsh-cleanup",
			"/Users/x/code/dotfiles/.claude/worktrees/156-zsh-cleanup"},
	}
	for _, c := range cases {
		// Resolver in tests is rooted at the temp dir.
		got, ok := DecodeSlugIn(c.slug, root)
		if !ok {
			t.Errorf("%s: not found", c.slug)
			continue
		}
		want := root + c.want
		if got != want {
			t.Errorf("%s = %s, want %s", c.slug, got, want)
		}
	}
}
