package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/devlog"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Health-check checklist",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
}

func runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ℹ build channel: %s\n", channel.Channel())
	cfg, err := config.Load(config.DefaultPath())
	check(out, "config loads", err == nil, err)
	env := deriveEnv(cfg)

	_, statErr := os.Stat(env.projectsRoot)
	check(out, "projects_root readable: "+env.projectsRoot, statErr == nil, statErr)

	fmt.Fprintf(out, "ℹ config path: %s\n", config.DefaultPath())
	fmt.Fprintf(out, "ℹ cache dir:   %s\n", env.cacheDir)
	c, cacheErr := cache.Open(cmd.Context(), env.dbPath)
	check(out, "cache db opens: "+env.dbPath, cacheErr == nil, cacheErr)
	if cacheErr == nil {
		defer c.Close()
		ok, ierr := c.IntegrityOK(cmd.Context())
		check(out, "integrity_check", ok && ierr == nil, ierr)
		reportMessageSpan(cmd.Context(), out, c)
	}

	hist, perr := pricing.Load()
	version := ""
	if perr == nil {
		version = hist.Latest().Version
	}
	check(out, "pricing loads (v="+version+")", perr == nil, perr)

	if cacheErr == nil && perr == nil {
		reportPricingVersions(cmd.Context(), out, c, hist)
	}

	checkCredential(out)
	reportCacheArtifacts(out, env.cacheDir)
	checkClaudeCodeHook(out)
	reportLogFile(out, env.cacheDir)
	return nil
}

// reportPricingVersions prints per-version row counts and stale tallies from the
// messages table. Best-effort: a query error prints one ERR line.
func reportPricingVersions(ctx context.Context, out io.Writer, c *cache.Cache, hist pricing.History) {
	pvStats, err := c.PricingVersionStats(ctx, hist)
	if err != nil {
		fmt.Fprintf(out, "  pricing versions: ERR %v\n", err)
		return
	}
	if len(pvStats) == 0 {
		return
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Pricing versions in DB:")
	for _, s := range pvStats {
		tag := ""
		if s.IsCurrent {
			tag = "  (current)"
		}
		line := fmt.Sprintf("  %s%s   %d rows", s.Version, tag, s.Rows)
		if s.Stale > 0 {
			line += fmt.Sprintf("  <- %d stale (auto-recost on next start)", s.Stale)
		}
		fmt.Fprintln(out, line)
	}
}

// checkCredential reports OAuth credential status without echoing token bytes.
func checkCredential(out io.Writer) {
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
}

// statCheck reports the presence of an optional file as an informational check.
// present builds the label when the file exists; missing is the label otherwise.
func statCheck(out io.Writer, path, missing string, present func(os.FileInfo) string) {
	if info, err := os.Stat(path); err == nil {
		check(out, present(info), true, nil)
	} else {
		check(out, missing, true, nil)
	}
}

// reportCacheArtifacts reports presence/age of the optional cache-dir files.
func reportCacheArtifacts(out io.Writer, cacheDir string) {
	statCheck(out, filepath.Join(cacheDir, "usage.json"), "usage cache: not present",
		func(info os.FileInfo) string {
			return fmt.Sprintf("usage cache: %s old", time.Since(info.ModTime()).Truncate(time.Second))
		})
	reportBackoffState(out, cacheDir, time.Now())
	reportParseErrors(out, cacheDir)
}

// reportBackoffState reports the persisted usage-API backoff window (#529).
//
// Informational, never a verdict: an open window is ccpulse behaving exactly
// as designed after a 429, and the only thing a user needs is the number of
// minutes before quota data refreshes again. The unreadable case gets its
// own line because Fetch silently ignores a corrupt state file — doctor is
// the one place that says so out loud.
func reportBackoffState(out io.Writer, cacheDir string, now time.Time) {
	st, err := anthro.ReadBackoffState(cacheDir, now)
	switch {
	case err != nil:
		fmt.Fprintf(out, "ℹ usage API backoff: state file unusable, ignored — %v\n", err)
	case !st.Active(now):
		fmt.Fprintln(out, "ℹ usage API backoff: none")
	default:
		fmt.Fprintf(out, "ℹ usage API backoff: retry in %s (%s)\n",
			st.RetryAt.Sub(now).Truncate(time.Second), backoffCause(st.Consecutive429))
	}
}

// backoffCause renders why the window is open. A transport failure or a 5xx
// opens one too, and those carry no 429s — "0 consecutive 429s" next to a
// live deadline reads as a contradiction, so that case says what actually
// happened instead of counting to zero.
func backoffCause(consecutive429 int) string {
	switch consecutive429 {
	case 0:
		return "last attempt failed; not rate-limited"
	case 1:
		return "1 consecutive 429"
	default:
		return fmt.Sprintf("%d consecutive 429s", consecutive429)
	}
}

// implausibleBefore is the floor below which `doctor` calls a message timestamp
// corrupt rather than merely old. Claude Code did not exist in 2020, so nothing
// older can be real history — which keeps the check free of any judgement about
// how old a genuine transcript is allowed to be. It catches both shapes seen in
// practice, since they are what a missing timestamp decays to: Go's zero time
// (year 1) and the Unix epoch (1970).
var implausibleBefore = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// reportMessageSpan prints the cache's time span and flags an implausible
// oldest row.
//
// This is the check that was missing during #527: one row stamped 0001-01-01
// stretched the chart's x-axis across two millennia and made the TUI unbootable,
// while every existing doctor line — integrity_check included — reported ✓,
// because the database file was structurally perfect. The span is the cheapest
// signal that says otherwise, and it names the remedy, since the rebuild is not
// something a user would guess at.
func reportMessageSpan(ctx context.Context, out io.Writer, c *cache.Cache) {
	span, ok, err := c.MessageSpanOf(ctx)
	if err != nil {
		check(out, "message span readable", false, err)
		return
	}
	if !ok {
		fmt.Fprintln(out, "ℹ cache: no messages indexed yet")
		return
	}
	fmt.Fprintf(out, "ℹ cache span: %s → %s (%d messages)\n",
		span.Earliest.Format(time.DateOnly), span.Latest.Format(time.DateOnly), span.Count)
	if span.Earliest.Before(implausibleBefore) {
		check(out, fmt.Sprintf(
			"message timestamps plausible — oldest row is %s, which predates Claude Code; "+
				"the chart axis is clamped. Fix: ccpulse index --rebuild",
			span.Earliest.Format(time.DateOnly)), false, nil)
		return
	}
	check(out, "message timestamps plausible", true, nil)
}

// reportParseErrors reports parse-errors.log by RECORD COUNT, not only size,
// and as an ℹ rather than a ✓.
//
// The old line printed "parse-errors.log: 2253900 bytes" with a ✓ next to it —
// asserting health about a file it had not read, while that file held ~14k
// records. doctor cannot tell a routine skip (a half-written line in a live
// session) from a real problem without classifying every record, so it states
// the number and declines to grade it. Count beats bytes because it is the
// figure a user can act on.
func reportParseErrors(out io.Writer, cacheDir string) {
	path := filepath.Join(cacheDir, "parse-errors.log")
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(out, "ℹ parse-errors.log: not present")
		return
	}
	age := time.Since(info.ModTime()).Truncate(time.Second)
	n, cerr := countLines(path)
	if cerr != nil {
		fmt.Fprintf(out, "ℹ parse-errors.log: %d bytes (%s old, unreadable: %v)\n", info.Size(), age, cerr)
		return
	}
	fmt.Fprintf(out, "ℹ parse-errors.log: %d records, %d bytes (%s old)\n", n, info.Size(), age)
}

// countLines counts newline-terminated records in path. The file is bounded by
// the ingester's 10MB rotation, so reading it inside a diagnostic command is
// affordable.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}

// reportLogFile prints the log file location and presence, honouring
// --log-level off (no file opened).
func reportLogFile(out io.Writer, cacheDir string) {
	if resolvedLogLevel == devlog.LevelOff {
		fmt.Fprintf(out, "ℹ log file: (disabled — --log-level off)\n")
		return
	}
	logName := "ccpulse.log"
	if channel.IsDev() {
		logName = "debug.log"
	}
	logPath := filepath.Join(cacheDir, logName)
	fmt.Fprintf(out, "ℹ log file: %s [level=%s]\n", logPath, logLevelFlag)
	statCheck(out, logPath, logName+": not present", func(info os.FileInfo) string {
		return fmt.Sprintf("%s: %d bytes (%s old)",
			logName, info.Size(), time.Since(info.ModTime()).Truncate(time.Second))
	})
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

// claudeStopHookSnippet is the copy-pasteable settings.json fragment that
// configures the recommended Stop hook. Both `doctor` and README.md print
// this same string verbatim; a drift-guard test in doctor_test.go enforces
// the README contains it as a substring.
const claudeStopHookSnippet = `{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "ccpulse status --quiet" }
        ]
      }
    ]
  }
}`

// checkClaudeCodeHook detects whether the user's global Claude Code
// settings.json (~/.claude/settings.json) configures a Stop hook that
// invokes ccpulse. Substring match on "ccpulse" — users may wrap the
// invocation in a shell function or script.
//
// All outcomes are informational; the check never fails the overall doctor
// report. Settings.json contents are NEVER echoed back — only the outcome
// line and the static snippet print.
func checkClaudeCodeHook(out io.Writer) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(out, "ℹ Claude Code settings.json: cannot resolve home dir: %v\n", err)
		return
	}
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "ℹ Claude Code settings.json not found")
			return
		}
		fmt.Fprintf(out, "ℹ Claude Code settings.json: %v\n", err)
		return
	}
	if hookCommandMentionsCcpulse(data) {
		fmt.Fprintln(out, "✓ ccpulse Stop hook detected")
		return
	}
	fmt.Fprintln(out, "✗ no ccpulse Stop hook")
	fmt.Fprintln(out, claudeStopHookSnippet)
}

// hookCommandMentionsCcpulse parses a settings.json byte slice and reports
// whether any Stop-hook command string contains "ccpulse". Tolerant of
// shape variations — returns false on parse errors rather than surfacing
// them, because the caller has already decided that broken JSON is just
// an info line.
func hookCommandMentionsCcpulse(data []byte) bool {
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return false
	}
	hooks, _ := top["hooks"].(map[string]any)
	stop, _ := hooks["Stop"].([]any)
	for _, entry := range stop {
		m, _ := entry.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, ok := hm["command"].(string); ok && strings.Contains(cmd, "ccpulse") {
				return true
			}
		}
	}
	return false
}
