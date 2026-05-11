package tui

import (
	"testing"

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
