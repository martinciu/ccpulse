package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
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
	"github.com/martinciu/ccpulse/pkg/secfile"
	"github.com/martinciu/ccpulse/pkg/tui"
	"github.com/martinciu/ccpulse/pkg/watcher"
)

var (
	version      = "dev"
	commit       = "none"
	date         = "unknown"
	buildChannel = "dev"
)

// newTeaProgram is the constructor for the TUI program. Tests
// override this to inject WithoutRenderer / WithInput / WithOutput
// options and exercise the full runTUI lifecycle without a real TTY.
var newTeaProgram = func(m tea.Model) *tea.Program {
	return tea.NewProgram(m, tea.WithAltScreen())
}

func versionString() string {
	if commit == "none" && date == "unknown" {
		return fmt.Sprintf("ccpulse %s (channel %s)", version, channel.Channel())
	}
	return fmt.Sprintf("ccpulse %s (commit %s, built %s, channel %s)",
		version, commit, date, channel.Channel())
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ccpulse",
		Short:         "Claude Code usage TUI dashboard",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context())
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
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	}
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Inspect / edit config"}
	c.AddCommand(&cobra.Command{
		Use:  "path",
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), config.DefaultPath())
		},
	})
	c.AddCommand(&cobra.Command{
		Use:  "show",
		Args: cobra.NoArgs,
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
		Use:  "edit",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultPath()
			if err := ensureConfigFile(path); err != nil {
				return err
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

// ensureConfigFile creates path with the default scaffold if missing,
// at FileMode under a DirMode parent. If the file exists already, only
// its mode is tightened.
func ensureConfigFile(path string) error {
	if err := secfile.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return os.Chmod(path, secfile.FileMode)
	} else if !os.IsNotExist(err) {
		return err
	}
	return secfile.WriteFile(path, defaultTOMLBytes())
}
// runTUI launches the Bubble Tea program with the TUI model. The
// passed ctx is the signal-aware root context — used as the parent
// for the quota poller's context and for the startup backfill, so
// SIGINT/SIGTERM cancels in-flight work even before the user quits
// the TUI itself.
func runTUI(ctx context.Context) error {
	cfg, _ := config.Load(config.DefaultPath())
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	if err := secfile.MkdirAll(cacheDir); err != nil {
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
	p := newTeaProgram(m)

	// Shutdown discipline: every long-running background goroutine
	// spawned below joins this WaitGroup. Defers below are ordered
	// (registration is reverse of execution) so that on return:
	//
	//   1. bfCancel signals the backfill ctx.
	//   2. bfDone.Wait blocks until the backfill goroutine returns.
	//   3. cancel signals the poller ctx (in-flight HTTP aborts).
	//   4. w.Close closes fsnotify, signalling the watcher goroutine.
	//   5. bg.Wait blocks until the watcher, poller, and initial-refresh
	//      goroutines all return — including any in-flight db.Exec from
	//      runQuotaPoller.push.
	//   6. c.Close closes the cache (registered earlier; runs last among
	//      the cache-touching defers).
	//
	// The backfill keeps a separate WaitGroup (bfDone) because it has
	// a distinct lifecycle (one-shot). Both bfCtx and the poller ctx
	// derive from the signal-aware root, so SIGINT/SIGTERM cancels
	// in-flight work immediately, not only after p.Run returns.
	var bg sync.WaitGroup

	pollerCtx, cancel := context.WithCancel(ctx)

	if hasOAuth {
		retention := time.Duration(cfg.History.RetentionDays) * 24 * time.Hour
		bg.Go(func() {
			runQuotaPoller(pollerCtx, p, cred, cacheDir, c, retention)
		})
	}

	bg.Go(func() {
		w.Run(func(path string) {
			_, _ = ing.ProcessFile(path)
			p.Send(tui.RefreshMsg{})
		})
	})

	bfCtx, bfCancel := context.WithCancel(ctx)
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

	// Kick off an initial refresh so the TUI shows current data on launch.
	bg.Go(func() {
		p.Send(tui.RefreshMsg{})
	})

	// Defer registration order matters — see the block comment above.
	// Registered LAST → runs FIRST; registered FIRST (further up) → runs LAST.
	defer bg.Wait()
	defer w.Close()
	defer cancel()
	defer bfDone.Wait()
	defer bfCancel()

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
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
