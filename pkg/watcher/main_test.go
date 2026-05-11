package watcher

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in package watcher under goleak so any
// goroutine that outlives a test is surfaced as a failure. No
// IgnoreTopFunction list — if a leak shows up, fix the test.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
