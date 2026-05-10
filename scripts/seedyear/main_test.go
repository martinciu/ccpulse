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

	_, _, err := runSeed(seedOpts{
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

	_, _, err := runSeed(seedOpts{
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

	inserted, total, err := runSeed(seedOpts{
		profile:  "light",
		cacheDir: cacheDir,
		seed:     1,
		days:     365,
	})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if inserted < 18000 || inserted > 32000 {
		t.Errorf("light 365d: inserted=%d, want [18000, 32000]", inserted)
	}
	if total != inserted {
		t.Errorf("first run: total=%d, inserted=%d (want equal)", total, inserted)
	}
}

func TestSeedYear_HeavyProfile_RowCountInRange(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 30 days × 100% × 3.5 sessions × 5.5h × 60s/row ≈ 34k rows.
	// Range is ~±25% to absorb RNG variance under a fixed seed.
	inserted, _, err := runSeed(seedOpts{
		profile:  "heavy",
		cacheDir: cacheDir,
		seed:     1,
		days:     30,
	})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if inserted < 25000 || inserted > 40000 {
		t.Errorf("heavy 30d: inserted=%d, want [25000, 40000]", inserted)
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

	inserted1, total1, err := runSeed(opts)
	if err != nil {
		t.Fatalf("first runSeed: %v", err)
	}
	if inserted1 == 0 {
		t.Fatalf("first run inserted 0 rows; nothing to test idempotency against")
	}

	inserted2, total2, err := runSeed(opts)
	if err != nil {
		t.Fatalf("second runSeed: %v", err)
	}
	if inserted2 != 0 {
		t.Errorf("idempotent: second run inserted=%d, want 0", inserted2)
	}
	if total1 != total2 {
		t.Errorf("idempotent: total changed %d -> %d", total1, total2)
	}
}
