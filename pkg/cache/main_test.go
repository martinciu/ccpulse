package cache

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs goleak.VerifyTestMain to surface any goroutine
// retained across test boundaries. Cache holds file handles and
// SQLite connections; any future regression where a goroutine
// retains the lock fd or DB handle would surface here.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
