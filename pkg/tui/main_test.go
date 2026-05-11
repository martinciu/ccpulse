package tui

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in package tui under goleak so leaks from
// bubbletea's program loop (introduced by teatest in integration_test.go)
// surface loudly instead of accumulating silently on CI.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
