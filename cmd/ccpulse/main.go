package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/martinciu/ccpulse/pkg/tui"
	"github.com/martinciu/ccpulse/pkg/watcher"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	if commit == "none" && date == "unknown" {
		return "ccpulse " + version
	}
	return fmt.Sprintf("ccpulse %s (commit %s, built %s)", version, commit, date)
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
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return err
	}
	// Integrity check; if the cache is corrupt, rebuild from scratch.
	// JSONL is the source of truth; SQLite is derived.
	if !c.IntegrityOK() {
		c.Close()
		_ = os.Remove(dbPath)
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

	m := tui.New(tui.Deps{
		Cache:         c,
		ProjectsRoot:  projectsRoot,
		HistoryDays:   cfg.History.DefaultWindowDays,
		Tier:          cfg.Plan.Tier,
		CeilingTokens: status.CeilingFor(cfg.Plan),
	})
	p := tea.NewProgram(m, tea.WithAltScreen())

	go w.Run(func(path string) {
		_, _ = ing.ProcessFile(path)
		p.Send(tui.RefreshMsg{})
	})

	// Kick off an initial refresh so the TUI shows current data on launch.
	go func() {
		p.Send(tui.RefreshMsg{})
	}()

	_, err = p.Run()
	return err
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
