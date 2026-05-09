package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/parse"
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
		// Tail-parse on event, write deltas, back-fill canonical,
		// then post a RefreshMsg.
		slug := slugFor(projectsRoot, path)
		_, off, line, _, _ := c.GetFile(path)
		msgs, perrs, newOff, newLine, err := parse.ParseFromOffsetWithErrors(path, slug, off, int(line))
		if err != nil {
			return
		}
		// Append parse errors to the rotated parse-errors.log.
		if len(perrs) > 0 {
			appendParseErrors(filepath.Join(cacheDir, "parse-errors.log"), path, perrs)
		}
		if len(msgs) == 0 {
			return
		}
		if err := c.InsertMessages(msgs, tab); err != nil {
			return
		}
		// New slug? Resolve and back-fill canonical for these rows.
		r, _ := res.Resolve(slug)
		if r.CanonicalPath != "" {
			_, _ = c.DB().Exec(
				`UPDATE messages SET project_canonical = ?, worktree_branch = ? WHERE project_slug = ? AND project_canonical = ''`,
				r.CanonicalPath, r.Branch, slug,
			)
		}
		st, _ := os.Stat(path)
		_ = c.RecordFile(path, st.ModTime().UnixNano(), newOff, int64(newLine))
		p.Send(tui.RefreshMsg{})
	})

	// Kick off an initial refresh so the TUI shows current data on launch.
	go func() {
		p.Send(tui.RefreshMsg{})
	}()

	// Background goroutine: fetch real block reset time from Anthropic API
	// (or its cache) and push AnthroMsg every 2 minutes.
	go func() {
		for {
			if d, err := anthro.Fetch(cacheDir); err == nil {
				p.Send(tui.AnthroMsg{ResetAt: d.SessionResetAt, Pct: d.Pct})
			}
			time.Sleep(2 * time.Minute)
		}
	}()

	_, err = p.Run()
	return err
}

const parseErrorsMaxBytes = 10 * 1024 * 1024 // 10 MB

// appendParseErrors writes per-line parse errors to a log file, rotating
// once the file exceeds 10 MB by truncating it and starting fresh.
// Best-effort — any error is swallowed.
func appendParseErrors(logPath, source string, perrs []parse.ParseError) {
	if info, err := os.Stat(logPath); err == nil && info.Size() > parseErrorsMaxBytes {
		_ = os.Remove(logPath)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, pe := range perrs {
		fmt.Fprintf(f, "%s:%d %v\n", source, pe.Line, pe.Err)
	}
}

// slugFor extracts the slug (top-level dir under projects root) from a path.
func slugFor(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
