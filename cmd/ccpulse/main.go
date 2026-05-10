package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/channel"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/devlog"
	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/tui"
	"github.com/martinciu/ccpulse/pkg/watcher"
)

var (
	version      = "dev"
	commit       = "none"
	date         = "unknown"
	buildChannel = "dev"
)

func versionString() string {
	if commit == "none" && date == "unknown" {
		return fmt.Sprintf("ccpulse %s (channel %s)", version, channel.Channel())
	}
	return fmt.Sprintf("ccpulse %s (commit %s, built %s, channel %s)",
		version, commit, date, channel.Channel())
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ccpulse",
		Short: "Claude Code usage TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.OutOrStdout())
		},
	}
	root.AddCommand(newStatusCmd())
	root.AddCommand(newIndexCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	}
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect / edit config"}
	c.AddCommand(&cobra.Command{
		Use: "path",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), config.DefaultPath())
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "show",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultPath()
			if _, err := os.Stat(path); os.IsNotExist(err) {
				path = ""
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			enc := toml.NewEncoder(cmd.OutOrStdout())
			return enc.Encode(cfg)
		},
	})
	c.AddCommand(&cobra.Command{
		Use: "edit",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultPath()
			if _, err := os.Stat(path); os.IsNotExist(err) {
				_ = os.MkdirAll(filepath.Dir(path), 0755)
				if err := os.WriteFile(path, defaultTOMLBytes(), 0644); err != nil {
					return err
				}
			}
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vim"
			}
			ed := exec.Command(editor, path)
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
			return ed.Run()
		},
	})
	return c
}

// defaultTOMLBytes — first-run scaffold; users edit, never overwritten by upgrades.
func defaultTOMLBytes() []byte {
	return []byte(`# ccpulse config — managed by you, never overwritten.
# See "ccpulse config show" for the live values (defaults + your overrides).
`)
}
// runTUI launches the Bubble Tea program with the TUI model. The `out`
// parameter is reserved for future use (currently the TUI manages its
// own terminal IO via the alt screen).
func runTUI(_ interface{}) error {
	cfg, _ := config.Load(config.DefaultPath())
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	if logCloser, err := devlog.Init(channel.IsDev(), cacheDir); err == nil && logCloser != nil {
		defer logCloser.Close()
	}
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return err
	}
	// Integrity check; if the cache is corrupt, rebuild from scratch.
	// JSONL is the source of truth; SQLite is derived.
	if !c.IntegrityOK() {
		c.Close()
		_ = cache.RemoveWithSiblings(dbPath)
		c, err = cache.Open(dbPath)
		if err != nil {
			return err
		}
	}
	defer c.Close()

	tab, _ := pricing.Load()
	if cfg.Pricing.Override != "" {
		if t, err := pricing.LoadFrom(expand(cfg.Pricing.Override)); err == nil {
			tab = t
		}
	}
	res := canonical.NewResolver(c, "/")

	projectsRoot := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))

	ing := &ingest.Ingester{
		Cache:          c,
		Resolver:       res,
		Pricing:        tab,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}

	w, err := watcher.New(projectsRoot)
	if err != nil {
		return err
	}
	defer w.Close()

	cred, credErr := anthro.LoadCredential()
	if credErr != nil && !errors.Is(credErr, anthro.ErrNoCredential) {
		fmt.Fprintf(os.Stderr, "ccpulse: %v\n", credErr)
	}
	hasOAuth := credErr == nil

	m := tui.New(tui.Deps{
		Cache:        c,
		ProjectsRoot: projectsRoot,
		Credential:   cred,
		HasOAuth:     hasOAuth,
		CacheDir:     cacheDir,
		IsDev:        channel.IsDev(),
	})
	p := tea.NewProgram(m, tea.WithAltScreen())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if hasOAuth {
		retention := time.Duration(cfg.History.RetentionDays) * 24 * time.Hour
		go runQuotaPoller(ctx, p, cred, cacheDir, c, retention)
	}

	go w.Run(func(path string) {
		_, _ = ing.ProcessFile(path)
		p.Send(tui.RefreshMsg{})
	})

	// Startup backfill: catch the cache up to EOF for every .jsonl
	// under projectsRoot. Runs concurrently with the watcher;
	// SQLite serialises writes, InsertMessages is idempotent, so
	// the worst case of overlap is wasted parse work.
	//
	// On shutdown the goroutine must finish before c.Close runs, or
	// an in-flight ProcessFile would write to a closed SQLite handle.
	// Defer order is LIFO, so registering Wait *after* Cancel makes
	// Cancel fire first (signal the goroutine), then Wait blocks
	// until the goroutine returns, then w.Close and c.Close run.
	bfCtx, bfCancel := context.WithCancel(context.Background())
	var bfDone sync.WaitGroup
	bfDone.Add(1)
	bf := &ingest.Backfill{Ingester: ing}
	go func() {
		defer bfDone.Done()
		_ = bf.Run(bfCtx, func(pr ingest.Progress) {
			p.Send(tui.IndexProgressMsg{Done: pr.Done, Total: pr.Total, Active: pr.Active})
			p.Send(tui.RefreshMsg{})
		})
	}()
	defer bfDone.Wait()
	defer bfCancel()

	// Kick off an initial refresh so the TUI shows current data on launch.
	go func() {
		p.Send(tui.RefreshMsg{})
	}()

	_, err = p.Run()
	return err
}

// runQuotaPoller fires once immediately, then every 2 minutes, fetching
// usage data and pushing QuotaMsg to the program. On a successful fetch
// where Source=="api", it also appends a row to usage_samples and (if
// retention > 0) prunes anything older than now-retention. All side
// effects are best-effort; errors are swallowed so the TUI quota stays
// up to date even if the cache is misbehaving.
func runQuotaPoller(
	ctx context.Context,
	p *tea.Program,
	cred anthro.Credential,
	cacheDir string,
	c *cache.Cache,
	retention time.Duration,
) {
	push := func() {
		res, err := anthro.Fetch(ctx, cred, cacheDir)
		if err != nil {
			return
		}
		if res.Source == "api" {
			_ = c.RecordUsageSample(res.Usage, res.UpdatedAt)
			if retention > 0 {
				_, _ = c.PruneUsageSamples(time.Now().Add(-retention))
			}
		}
		p.Send(tui.QuotaMsg{
			Usage:     &res.Usage,
			Source:    res.Source,
			UpdatedAt: res.UpdatedAt,
		})
	}
	push() // immediate first tick
	t := time.NewTicker(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			push()
		}
	}
}

func main() {
	channel.Set(buildChannel)
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
