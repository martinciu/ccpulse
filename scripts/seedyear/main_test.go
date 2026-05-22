package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedYear_RejectsReleaseCacheDir(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse") // no -dev suffix
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runSeed(seedOpts{
		profile:  "light",
		cacheDir: cacheDir,
		seed:     1,
		days:     1,
	})
	if err == nil {
		t.Fatal("runSeed: expected error for non-dev cache dir, got nil")
	}
	if !strings.Contains(err.Error(), "-dev") {
		t.Errorf("runSeed: error %q does not mention '-dev'", err)
	}

	if _, err := os.Stat(filepath.Join(cacheDir, "state.db")); err == nil {
		t.Error("state.db was created despite path-guard rejection")
	}
}

func TestSeedYear_RejectsUnknownProfile(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runSeed(seedOpts{
		profile:  "garbage",
		cacheDir: cacheDir,
		seed:     1,
		days:     1,
	})
	if err == nil {
		t.Fatal("runSeed: expected error for unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), "garbage") {
		t.Errorf("runSeed: error %q does not name the bad profile", err)
	}
}

func TestSeedYear_LightProfile_RowCountInRange(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := runSeed(seedOpts{
		profile:  "light",
		cacheDir: cacheDir,
		seed:     1,
		days:     365,
	})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if res.msgsInserted < 18000 || res.msgsInserted > 32000 {
		t.Errorf("light 365d: msgsInserted=%d, want [18000, 32000]", res.msgsInserted)
	}
	if res.msgsTotal != res.msgsInserted {
		t.Errorf("first run: msgsTotal=%d, msgsInserted=%d (want equal)", res.msgsTotal, res.msgsInserted)
	}
}

func TestSeedYear_HeavyProfile_RowCountInRange(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 30 days × 100% × 3.5 sessions × 5.5h × 60s/row ≈ 34k rows.
	// Range is ~±25% to absorb RNG variance under a fixed seed.
	res, err := runSeed(seedOpts{
		profile:  "heavy",
		cacheDir: cacheDir,
		seed:     1,
		days:     30,
	})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if res.msgsInserted < 25000 || res.msgsInserted > 40000 {
		t.Errorf("heavy 30d: msgsInserted=%d, want [25000, 40000]", res.msgsInserted)
	}
}

func TestSeedYear_Idempotent(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := seedOpts{
		profile:  "light",
		cacheDir: cacheDir,
		seed:     1,
		days:     30,
	}

	res1, err := runSeed(opts)
	if err != nil {
		t.Fatalf("first runSeed: %v", err)
	}
	if res1.msgsInserted == 0 {
		t.Fatalf("first run inserted 0 rows; nothing to test idempotency against")
	}

	res2, err := runSeed(opts)
	if err != nil {
		t.Fatalf("second runSeed: %v", err)
	}
	if res2.msgsInserted != 0 {
		t.Errorf("idempotent: second run msgsInserted=%d, want 0", res2.msgsInserted)
	}
	if res1.msgsTotal != res2.msgsTotal {
		t.Errorf("idempotent: msgsTotal changed %d -> %d", res1.msgsTotal, res2.msgsTotal)
	}
}
