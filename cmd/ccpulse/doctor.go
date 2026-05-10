package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
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
			fmt.Fprintf(out, "ℹ build channel: %s\n", channel.Channel())
			cfg, err := config.Load(config.DefaultPath())
			check(out, "config loads", err == nil, err)

			projects := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))
			_, statErr := os.Stat(projects)
			check(out, "projects_root readable: "+projects, statErr == nil, statErr)

			cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
			fmt.Fprintf(out, "ℹ config path: %s\n", config.DefaultPath())
			fmt.Fprintf(out, "ℹ cache dir:   %s\n", cacheDir)
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

			// OAuth credential check
			cred, credErr := anthro.LoadCredential()
			switch {
			case errors.Is(credErr, anthro.ErrNoCredential):
				check(out, "OAuth credential: not found (cost mode)", true, nil)
			case credErr != nil:
				check(out, "OAuth credential", false, credErr)
			case cred.Expired(time.Now()):
				check(out, "OAuth credential: EXPIRED — run /login in claude", false, nil)
			default:
				check(out, fmt.Sprintf("OAuth credential: %s (%s)", anthro.TierPretty(cred.RateLimitTier), cred.SubscriptionType), true, nil)
			}

			// usage cache check
			usagePath := filepath.Join(cacheDir, "usage.json")
			if info, err := os.Stat(usagePath); err == nil {
				age := time.Since(info.ModTime()).Truncate(time.Second)
				check(out, fmt.Sprintf("usage cache: %s old", age), true, nil)
			} else {
				check(out, "usage cache: not present", true, nil)
			}

			parseErrPath := filepath.Join(cacheDir, "parse-errors.log")
			if info, err := os.Stat(parseErrPath); err == nil {
				check(out, fmt.Sprintf("parse-errors.log: %d bytes (%s old)",
					info.Size(), time.Since(info.ModTime()).Truncate(time.Second)), true, nil)
			} else {
				check(out, "parse-errors.log: not present", true, nil)
			}

			if channel.IsDev() {
				debugPath := filepath.Join(cacheDir, "debug.log")
				if info, err := os.Stat(debugPath); err == nil {
					check(out, fmt.Sprintf("debug.log: %d bytes (%s old)",
						info.Size(), time.Since(info.ModTime()).Truncate(time.Second)), true, nil)
				} else {
					check(out, "debug.log: not present", true, nil)
				}
			}

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
