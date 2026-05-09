package canonical

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestResolveAndMemoize(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := exec.Command("mkdir", "-p", repo).Run(); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	c, _ := cache.Open(filepath.Join(dir, "s.db"))
	defer c.Close()

	// Encode the repo path back into a slug-style string.
	// "/var/folders/.../repo" -> "-var-folders-...-repo".
	slug := "-" + strings.TrimPrefix(strings.ReplaceAll(repo, "/", "-"), "-")
	r := NewResolver(c, "/")

	res, err := r.Resolve(slug)
	if err != nil {
		t.Fatal(err)
	}
	// Use EvalSymlinks for the comparison since macOS /var → /private/var.
	wantRepo, _ := filepath.EvalSymlinks(repo)
	if res.CanonicalPath != wantRepo {
		t.Errorf("CanonicalPath = %s, want %s", res.CanonicalPath, wantRepo)
	}

	// Hit the cache on the second call (no git invoked).
	res2, _ := r.Resolve(slug)
	if res2.CanonicalPath != wantRepo {
		t.Errorf("memoized CanonicalPath = %s", res2.CanonicalPath)
	}
}
