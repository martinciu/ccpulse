package anthro

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in package anthro under goleak so any
// goroutine that outlives a test is surfaced as a failure. No
// IgnoreTopFunction list — if a leak shows up, fix the test.
// IgnoreCurrent snapshots pre-test goroutines (testing harness,
// runtime helpers) so future package init() routines don't get
// misreported as leaks.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}
