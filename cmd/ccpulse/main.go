package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
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

// logLevelFlag is the raw value of --log-level after cobra parses.
// resolvedLogLevel is the slog.Level it parsed to; written by
// PersistentPreRunE on the root cmd, read by runTUI and doctor.
//
// Initialized to devlog.LevelOff so that if anything ever bypasses
// PersistentPreRunE (a direct runTUI call from a test, a future
// subcommand that replaces the root's PersistentPreRunE), the failure
// mode is "silent log handler, no file opened" rather than "wrong
// level + a file gets opened against the user's intent".
var (
	logLevelFlag     string
	resolvedLogLevel = devlog.LevelOff
)

// defaultLogLevelFlag returns the channel-aware default for --log-level.
// Read at flag-registration time; channel.Set(buildChannel) must have run
// before newRootCmd() is called (main() guarantees this).
func defaultLogLevelFlag() string {
	if channel.IsDev() {
		return "debug"
	}
	return "info"
}

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
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			lvl, err := devlog.ParseLevel(logLevelFlag)
			if err != nil {
				return fmt.Errorf("--log-level: %w", err)
			}
			resolvedLogLevel = lvl
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), cmd.ErrOrStderr())
		},
	}
	root.PersistentFlags().StringVar(
		&logLevelFlag,
		"log-level",
		defaultLogLevelFlag(),
		"logging verbosity: off | error | warn | info | debug",
	)
	root.AddCommand(newStatusCmd())
	root.AddCommand(newIndexCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newRecostCmd())
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
// Delegates to pkg/config so the scaffold stays in lockstep with resolved
// defaults (single source of truth for both in-code defaults and the
// live-value config the user edits in place).
func defaultTOMLBytes() []byte {
	return config.Scaffold()
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

// initDevlog wraps devlog.Init and surfaces failures to w (typically
// os.Stderr) along with a remediation hint. Devlog is best-effort, so
// errors are non-fatal — they only mean slog output is now going to
// io.Discard for the rest of the run.
func initDevlog(isDev bool, cacheDir string, level slog.Level, w io.Writer) io.Closer {
	closer, err := devlog.Init(devlog.Options{
		IsDev:    isDev,
		CacheDir: cacheDir,
		Level:    level,
	})
	if err != nil {
		fmt.Fprintf(w, "devlog init failed: %v (log disabled; check %s permissions)\n", err, cacheDir)
	}
	return closer
}

// watcherStartupError translates a watcher.New failure into a user-facing
// message at the CLI boundary. A missing projects root (fs.ErrNotExist) gets
// an actionable hint; every other error passes through unchanged — it has
// already been wrapped with the path by watcher.New.
func watcherStartupError(projectsRoot string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ccpulse: %s: no such file or directory — run Claude Code at least once, or set CCPULSE_PROJECTS_ROOT", projectsRoot)
	}
	return err
}

// runTUI launches the Bubble Tea program with the TUI model. The
// passed ctx is the signal-aware root context — used as the parent
// for the quota poller's context and for the startup backfill, so
// SIGINT/SIGTERM cancels in-flight work even before the user quits
// the TUI itself.
func runTUI(ctx context.Context, errOut io.Writer) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load config %s: %w", config.DefaultPath(), err)
	}
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	if err := secfile.MkdirAll(cacheDir); err != nil {
		return err
	}
	if logCloser := initDevlog(channel.IsDev(), cacheDir, resolvedLogLevel, os.Stderr); logCloser != nil {
		defer logCloser.Close()
	}
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := openCacheWithRebuild(dbPath, errOut)
	if err != nil {
		return err
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return err
	}
	projectsRoot := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))
	c.AutoRecost(ctx, hist)

	ing := &ingest.Ingester{
		Cache:          c,
		Pricing:        hist,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}

	w, err := watcher.New(projectsRoot)
	if err != nil {
		return watcherStartupError(projectsRoot, err)
	}

	qs := resolveQuotaStartup(errOut, time.Now())

	m := tui.New(tui.Deps{
		Cache:        c,
		ProjectsRoot: projectsRoot,
		Credential:   qs.cred,
		HasOAuth:     qs.hasOAuth,
		CacheDir:     cacheDir,
		IsDev:        channel.IsDev(),
		ReduceMotion: cfg.UI.ReduceMotion,
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

	switch {
	case qs.fakeQuota:
		// Push the synthetic quota once; no poller, no network, no
		// usage.json / usage_samples writes.
		bg.Go(func() {
			p.Send(tui.QuotaMsg{
				Usage:     qs.fakeUsage,
				Source:    "cache_fresh",
				UpdatedAt: time.Now().UTC(),
			})
		})
	case qs.hasOAuth:
		retention := time.Duration(cfg.History.RetentionDays) * 24 * time.Hour
		bg.Go(func() {
			runQuotaPoller(pollerCtx, p, qs.cred, cacheDir, c, retention)
		})
	}

	bg.Go(func() {
		w.Run(func(path string) {
			_, _ = ing.ProcessFile(path)
			p.Send(tui.RefreshMsg{})
		})
	})

	bfCancel, bfDone := startBackfill(ctx, p, ing)

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

// openCacheWithRebuild opens the cache at dbPath, rebuilding from JSONL if the
// integrity check fails. ErrLockHeld on either path prints an actionable hint to
// errOut. The caller owns the returned cache (defer Close).
func openCacheWithRebuild(dbPath string, errOut io.Writer) (*cache.Cache, error) {
	c, err := cache.Open(dbPath)
	if err != nil {
		if errors.Is(err, cache.ErrLockHeld) {
			fmt.Fprintln(errOut,
				"ccpulse: cache locked by another ccpulse process (likely `index --rebuild`). Retry shortly.")
		}
		return nil, err
	}
	// Integrity check; if the cache is corrupt, rebuild from scratch. JSONL is
	// the source of truth; SQLite is derived.
	if c.IntegrityOK() {
		return c, nil
	}
	c.Close()
	c, err = cache.LockedRebuild(dbPath)
	if err != nil {
		if errors.Is(err, cache.ErrLockHeld) {
			fmt.Fprintln(errOut,
				"ccpulse: cache integrity check failed and rebuild blocked by another ccpulse process. Close the other instance and retry.")
		}
		return nil, err
	}
	return c, nil
}

// quotaStartup is the resolved credential + demo-seam state used to wire the
// quota source.
type quotaStartup struct {
	cred      anthro.Credential
	hasOAuth  bool
	fakeUsage *anthro.Usage
	fakeQuota bool
}

// resolveQuotaStartup loads the OAuth credential (best-effort) and applies the
// CCPULSE_FAKE_QUOTA demo seam (#265). Credential errors other than
// ErrNoCredential are surfaced to errOut.
func resolveQuotaStartup(errOut io.Writer, now time.Time) quotaStartup {
	cred, credErr := anthro.LoadCredential()
	if credErr != nil && !errors.Is(credErr, anthro.ErrNoCredential) {
		fmt.Fprintf(errOut, "ccpulse: %v\n", credErr)
	}
	s := quotaStartup{cred: cred, hasOAuth: credErr == nil}

	fakeUsage, fakeTier, fakeQuota := parseFakeQuota(
		os.Getenv("CCPULSE_FAKE_QUOTA"), os.Getenv("CCPULSE_FAKE_TIER"), now)
	if fakeQuota {
		s.cred = anthro.Credential{RateLimitTier: fakeTier}
		s.hasOAuth = true
		s.fakeUsage = fakeUsage
		s.fakeQuota = true
	}
	return s
}

// startBackfill launches the one-shot cold-walk backfill on its own context and
// WaitGroup. The returned cancel + wg MUST be wired into runTUI's deferred
// shutdown (cancel before wg.Wait) — see the defer-ordering block in runTUI.
func startBackfill(ctx context.Context, p *tea.Program, ing *ingest.Ingester) (context.CancelFunc, *sync.WaitGroup) {
	bfCtx, bfCancel := context.WithCancel(ctx)
	var bfDone sync.WaitGroup
	bfDone.Add(1)
	bf := &ingest.Backfill{Ingester: ing}
	go func() {
		defer bfDone.Done()
		_ = bf.Run(bfCtx, func(pr ingest.Progress) {
			p.Send(tui.IndexProgressMsg{Done: pr.Done, Total: pr.Total, Active: pr.Active})
			if !pr.Active {
				// Coalesce backfill-driven repaints into a single RefreshMsg at
				// completion. Watcher writes during backfill still drive their
				// own refreshes. See issue #94.
				p.Send(tui.RefreshMsg{})
			}
		})
	}()
	return bfCancel, &bfDone
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
			slog.Warn("ccpulse.quotaPoller",
				"outcome", "fetch_error",
				"err", err)
			return
		}
		if res.Source == "cache_stale" {
			slog.Warn("ccpulse.quotaPoller",
				"outcome", "cache_stale",
				"cache_age_s", int(time.Since(res.UpdatedAt).Seconds()))
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

// exitCodeFor maps a top-level error to a Unix exit code. Defaults
// to 1 (general error); ErrLockHeld maps to 75 (BSD sysexits
// EX_TEMPFAIL — "temporary failure, retry possible").
func exitCodeFor(err error) int {
	if errors.Is(err, cache.ErrLockHeld) {
		return 75
	}
	return 1
}

func main() {
	channel.Set(buildChannel)
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	// stop() unconditionally, not via defer: the error path calls os.Exit,
	// which would skip a deferred stop() (gocritic exitAfterDefer).
	err := newRootCmd().ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}
