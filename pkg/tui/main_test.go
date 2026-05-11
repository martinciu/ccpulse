package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"go.uber.org/goleak"
)

// TestMain runs every test in package tui under goleak so leaks from
// bubbletea's program loop (introduced by teatest in integration_test.go)
// surface loudly instead of accumulating silently on CI.
//
// teatest.NewTestModel spawns a SIGINT-handler goroutine that blocks on
// a channel receive and only exits on SIGINT — it is benign during tests
// but indistinguishable from a real leak to goleak. Exclude it by name.
//
// The func2 suffix is Go's closure-numbering for teatest's NewTestModel
// internals. If goleak starts firing after a teatest upgrade, re-verify
// which closure now owns the SIGINT receive and update the symbol name.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/charmbracelet/x/exp/teatest.NewTestModel.func2"),
	)
}

// withForcedColor sets the lipgloss color profile to TrueColor for the
// duration of a test, restoring it on cleanup. Use in tests that assert
// on rendered ANSI escapes — `go test` runs without a TTY, so lipgloss
// auto-detects and strips colors by default.
//
// Restoring is essential: a leaked TrueColor profile breaks other tests
// in this package that match on plain substrings ("q quit", "[DEV]")
// which become split across escape spans under TrueColor.
//
// NOT SAFE WITH t.Parallel(): lipgloss.SetColorProfile mutates the
// process-global DefaultRenderer. If two parallel tests both call this
// helper, their Cleanups will race against each other's assertions and
// the second restore can flip the profile mid-render of the first.
// No test in this package currently calls t.Parallel(), so this is a
// latent footgun rather than an active bug. If adding t.Parallel() to
// any tui test, refactor this helper to use a private
// lipgloss.NewRenderer instead of mutating the global.
func withForcedColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}
