package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Health-check checklist",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.Load(config.DefaultPath())
			check(out, "config loads", err == nil, err)

			projects := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))
			_, statErr := os.Stat(projects)
			check(out, "projects_root readable: "+projects, statErr == nil, statErr)

			cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
			dbPath := filepath.Join(cacheDir, "state.db")
			c, err := cache.Open(dbPath)
			check(out, "cache db opens: "+dbPath, err == nil, err)
			if err == nil {
				_, ierr := c.DB().Exec(`PRAGMA integrity_check`)
				check(out, "integrity_check", ierr == nil, ierr)
				c.Close()
			}

			tab, err := pricing.Load()
			check(out, "pricing loads (v="+tab.Version+")", err == nil, err)

			_, gitErr := exec.LookPath("git")
			check(out, "git on PATH", gitErr == nil, gitErr)
			_, tmuxErr := exec.LookPath("tmux")
			check(out, "tmux on PATH", tmuxErr == nil, tmuxErr)
			return nil
		},
	}
}

func check(out io.Writer, msg string, ok bool, err error) {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	fmt.Fprintf(out, "%s %s", mark, msg)
	if err != nil {
		fmt.Fprintf(out, " — %v", err)
	}
	fmt.Fprintln(out, "")
}
