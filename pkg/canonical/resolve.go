package canonical

import (
	"path/filepath"
	"strings"

	"github.com/martinciu/ccpulse/pkg/cache"
)

type Resolver struct {
	c    *cache.Cache
	root string
}

func NewResolver(c *cache.Cache, root string) *Resolver {
	return &Resolver{c: c, root: root}
}

type Resolved struct {
	Slug          string
	CanonicalPath string
	Branch        string
	Resolved      bool
}

func (r *Resolver) Resolve(slug string) (Resolved, error) {
	if got, ok, _ := r.c.GetSlugCanonical(slug); ok {
		return Resolved{
			Slug: got.Slug, CanonicalPath: got.CanonicalPath,
			Branch: got.Branch, Resolved: got.Resolved,
		}, nil
	}

	res := Resolved{Slug: slug}

	path, ok := DecodeSlugIn(slug, r.root)
	if !ok {
		res.CanonicalPath = slug
		_ = r.c.PutSlugCanonical(cache.SlugCanonical{
			Slug: slug, CanonicalPath: slug, Resolved: false,
		})
		return res, nil
	}

	gi, err := ResolveGit(path)
	if err != nil {
		// Not a git repo, or git unavailable: use the decoded path.
		res.CanonicalPath = path
		res.Resolved = false
		_ = r.c.PutSlugCanonical(cache.SlugCanonical{
			Slug: slug, CanonicalPath: path, Resolved: false,
		})
		return res, nil
	}

	canonical := gi.Root
	if gi.IsWorktree {
		// Main repo path = parent of common-dir's "/.git"
		canonical = filepath.Dir(strings.TrimSuffix(gi.CommonDir, "/.git"))
	}
	res.CanonicalPath = canonical
	res.Branch = gi.Branch
	res.Resolved = true
	_ = r.c.PutSlugCanonical(cache.SlugCanonical{
		Slug: slug, CanonicalPath: canonical, Branch: gi.Branch, Resolved: true,
	})
	return res, nil
}
