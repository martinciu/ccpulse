package status

import (
	"testing"
	"time"
)

// TestComputeThroughput_SteadyState seeds several in-window messages (with
// non-zero cache columns to prove they do NOT leak into the input+output
// headline) plus one out-of-window message that must be excluded, then checks
// the sums and the ×2 extrapolation.
func TestComputeThroughput_SteadyState(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	insertMsg(t, db, now.Add(-5*time.Minute), 100, 10, 7, 8, 9, 0.20)
	insertMsg(t, db, now.Add(-15*time.Minute), 200, 20, 7, 8, 9, 0.30)
	insertMsg(t, db, now.Add(-29*time.Minute), 300, 30, 7, 8, 9, 0.46)
	// 40 min ago → outside the 30-min window, excluded.
	insertMsg(t, db, now.Add(-40*time.Minute), 999, 999, 0, 0, 0, 9.99)

	th, err := ComputeThroughput(t.Context(), db, now)
	if err != nil {
		t.Fatalf("ComputeThroughput: %v", err)
	}

	// input+output only: (100+10)+(200+20)+(300+30) = 660 (cache excluded).
	if got, want := th.TokensInWindow, int64(660); got != want {
		t.Errorf("TokensInWindow = %d, want %d (input+output only, cache excluded)", got, want)
	}
	if got, want := th.TokensPerHour, int64(1320); got != want {
		t.Errorf("TokensPerHour = %d, want %d (×2 extrapolation)", got, want)
	}
	if !approxEqual(th.CostInWindowUSD, 0.96) {
		t.Errorf("CostInWindowUSD = %v, want 0.96", th.CostInWindowUSD)
	}
	if !approxEqual(th.CostPerHourUSD, 1.92) {
		t.Errorf("CostPerHourUSD = %v, want 1.92", th.CostPerHourUSD)
	}
	if th.WindowMinutes != 30 {
		t.Errorf("WindowMinutes = %d, want 30", th.WindowMinutes)
	}
}

// TestComputeThroughput_Idle proves an empty messages table yields a non-nil,
// fully-zeroed Throughput (the COALESCE NULL→0 path) with WindowMinutes still 30.
func TestComputeThroughput_Idle(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	th, err := ComputeThroughput(t.Context(), db, now)
	if err != nil {
		t.Fatalf("ComputeThroughput: %v", err)
	}
	if th == nil {
		t.Fatal("ComputeThroughput returned nil; want zeroed Throughput")
	}
	if th.TokensInWindow != 0 || th.TokensPerHour != 0 ||
		th.CostInWindowUSD != 0 || th.CostPerHourUSD != 0 {
		t.Errorf("idle window not zeroed: %+v", th)
	}
	if th.WindowMinutes != 30 {
		t.Errorf("WindowMinutes = %d, want 30 even when idle", th.WindowMinutes)
	}
}

// TestComputeThroughput_ShortHistory is the regression guard for the
// fixed-denominator decision: a single burst 5 min into the window extrapolates
// as in-window-total × 2, NOT normalized to the 5-min elapsed (which would
// over-report wildly).
func TestComputeThroughput_ShortHistory(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	insertMsg(t, db, now.Add(-5*time.Minute), 1000, 200, 0, 0, 0, 2.0)

	th, err := ComputeThroughput(t.Context(), db, now)
	if err != nil {
		t.Fatalf("ComputeThroughput: %v", err)
	}
	if got, want := th.TokensInWindow, int64(1200); got != want {
		t.Errorf("TokensInWindow = %d, want %d", got, want)
	}
	// Fixed 30-min denominator: 1200 × 2 = 2400/hr. NOT elapsed-normalized
	// (5-min elapsed would give 1200 × 12 = 14400/hr).
	if got, want := th.TokensPerHour, int64(2400); got != want {
		t.Errorf("TokensPerHour = %d, want %d (fixed denominator, not elapsed-normalized)", got, want)
	}
}

// TestComputeThroughput_WindowBoundary pins the inclusive >= lower bound: a row
// at exactly now-30m is inside; one a millisecond older is outside.
func TestComputeThroughput_WindowBoundary(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// Exactly at now-30m → inside (ts >= cutoff).
	insertMsg(t, db, now.Add(-throughputWindow), 100, 0, 0, 0, 0, 0.0)
	// One millisecond older → outside (tsFormat has ms precision).
	insertMsg(t, db, now.Add(-throughputWindow-time.Millisecond), 500, 0, 0, 0, 0, 0.0)

	th, err := ComputeThroughput(t.Context(), db, now)
	if err != nil {
		t.Fatalf("ComputeThroughput: %v", err)
	}
	if got, want := th.TokensInWindow, int64(100); got != want {
		t.Errorf("TokensInWindow = %d, want %d (boundary row included, 1ms-older excluded)", got, want)
	}
}
