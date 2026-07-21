package status

import (
	"strings"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

func strPtr(s string) *string { return &s }

// scopedUsage builds a Usage whose Limits array mirrors the observed #458
// probe shape: session + weekly_all (unscoped) + one scoped weekly entry.
func scopedUsage(resetsAt *time.Time) *anthro.Usage {
	return &anthro.Usage{
		Limits: []anthro.Limit{
			{Kind: "session", Group: "session", Percent: 8, Severity: "normal"},
			{Kind: "weekly_all", Group: "weekly", Percent: 22, Severity: "normal"},
			{
				Kind: "weekly_scoped", Group: "weekly", Percent: 35,
				Severity: "normal", ResetsAt: resetsAt, IsActive: true,
				Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("Fable")}},
			},
		},
	}
}

func TestDistillScopedLimits(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	reset := now.Add(5*24*time.Hour + 12*time.Hour)

	t.Run("nil usage", func(t *testing.T) {
		if got := distillScopedLimits(nil, now); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("no limits array", func(t *testing.T) {
		if got := distillScopedLimits(&anthro.Usage{}, now); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("unscoped kinds filtered out", func(t *testing.T) {
		got := distillScopedLimits(scopedUsage(&reset), now)
		if len(got) != 1 {
			t.Fatalf("want 1 entry, got %d: %+v", len(got), got)
		}
		sl := got[0]
		if sl.Kind != "weekly_scoped" || sl.Model != "Fable" || sl.Percent != 35 ||
			!sl.IsActive || sl.Severity != "normal" {
			t.Errorf("distilled entry mismatch: %+v", sl)
		}
		if sl.MinutesToReset == nil || *sl.MinutesToReset != int((5*24+12)*60) {
			t.Errorf("MinutesToReset: %v, want %d", sl.MinutesToReset, (5*24+12)*60)
		}
	})

	t.Run("nil resets_at leaves MinutesToReset nil", func(t *testing.T) {
		got := distillScopedLimits(scopedUsage(nil), now)
		if len(got) != 1 || got[0].MinutesToReset != nil {
			t.Errorf("want nil MinutesToReset, got %+v", got)
		}
	})

	t.Run("past resets_at clamps to zero", func(t *testing.T) {
		past := now.Add(-time.Hour)
		got := distillScopedLimits(scopedUsage(&past), now)
		if len(got) != 1 || got[0].MinutesToReset == nil || *got[0].MinutesToReset != 0 {
			t.Errorf("want *0, got %+v", got)
		}
	})

	t.Run("missing display_name skipped", func(t *testing.T) {
		u := &anthro.Usage{Limits: []anthro.Limit{
			{Kind: "weekly_scoped", Percent: 40},                              // nil Scope
			{Kind: "weekly_scoped", Percent: 41, Scope: &anthro.LimitScope{}}, // nil Model
			{Kind: "weekly_scoped", Percent: 42, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{}}},                        // nil DisplayName
			{Kind: "weekly_scoped", Percent: 43, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("")}}}, // empty
		}}
		if got := distillScopedLimits(u, now); got != nil {
			t.Errorf("want nil (all skipped), got %+v", got)
		}
	})

	t.Run("percent rounded and clamped", func(t *testing.T) {
		u := &anthro.Usage{Limits: []anthro.Limit{
			{Kind: "weekly_scoped", Percent: 35.6, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("A")}}},
			{Kind: "weekly_scoped", Percent: 140, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("B")}}},
			{Kind: "weekly_scoped", Percent: -5, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("C")}}},
		}}
		got := distillScopedLimits(u, now)
		if len(got) != 3 || got[0].Percent != 36 || got[1].Percent != 100 || got[2].Percent != 0 {
			t.Errorf("round/clamp mismatch: %+v", got)
		}
	})

	t.Run("API order preserved", func(t *testing.T) {
		u := &anthro.Usage{Limits: []anthro.Limit{
			{Kind: "weekly_scoped", Percent: 10, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("Zeta")}}},
			{Kind: "weekly_scoped", Percent: 90, Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: strPtr("Alpha")}}},
		}}
		got := distillScopedLimits(u, now)
		if len(got) != 2 || got[0].Model != "Zeta" || got[1].Model != "Alpha" {
			t.Errorf("order not preserved: %+v", got)
		}
	})
}

func TestComputePopulatesScopedLimits(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	reset := now.Add(24 * time.Hour)
	db := freshDB(t)
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: scopedUsage(&reset)})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ScopedLimits) != 1 || w.ScopedLimits[0].Model != "Fable" {
		t.Errorf("Window.ScopedLimits: %+v", w.ScopedLimits)
	}
}

func TestScopedLimitsJSON(t *testing.T) {
	t.Run("present when populated", func(t *testing.T) {
		mins := 300
		out, err := JSON(Window{ScopedLimits: []ScopedLimit{{
			Kind: "weekly_scoped", Model: "Fable", Percent: 35,
			IsActive: true, Severity: "normal", MinutesToReset: &mins,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`"scoped_limits":[`, `"kind":"weekly_scoped"`, `"model":"Fable"`,
			`"percent":35`, `"is_active":true`, `"severity":"normal"`, `"minutes_to_reset":300`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("JSON missing %s in %s", want, out)
			}
		}
	})
	t.Run("omitted when empty", func(t *testing.T) {
		out, err := JSON(Window{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "scoped_limits") {
			t.Errorf("scoped_limits should be omitted when empty: %s", out)
		}
	})
}
