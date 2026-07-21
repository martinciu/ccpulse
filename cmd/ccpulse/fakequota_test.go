package main

import (
	"testing"
	"time"
)

func TestParseFakeQuota_Valid(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	u, tier, ok := parseFakeQuota("55,42", "", now)
	if !ok {
		t.Fatal("expected ok=true for valid input")
	}
	if u == nil || u.FiveHour == nil || u.SevenDay == nil {
		t.Fatal("expected both buckets populated")
	}
	if u.FiveHour.Utilization != 55 || u.SevenDay.Utilization != 42 {
		t.Errorf("utilizations: got 5h=%v 7d=%v want 55/42",
			u.FiveHour.Utilization, u.SevenDay.Utilization)
	}
	if u.FiveHour.ResetsAt == nil || u.SevenDay.ResetsAt == nil {
		t.Fatal("expected non-nil ResetsAt on both buckets")
	}
	if !u.FiveHour.ResetsAt.After(now) || !u.SevenDay.ResetsAt.After(now) {
		t.Error("expected ResetsAt in the future")
	}
	if tier != defaultFakeTier {
		t.Errorf("tier: got %q want default %q", tier, defaultFakeTier)
	}
}

func TestParseFakeQuota_TierOverride(t *testing.T) {
	_, tier, ok := parseFakeQuota("10,20", "max_5x", time.Now())
	if !ok || tier != "max_5x" {
		t.Errorf("got ok=%v tier=%q want true/max_5x", ok, tier)
	}
}

func TestParseFakeQuota_Invalid(t *testing.T) {
	now := time.Now()
	cases := []string{"", "  ", "55", "55,42,1", "x,2", "55,y", "-1,40", "55,101"}
	for _, in := range cases {
		if _, _, ok := parseFakeQuota(in, "", now); ok {
			t.Errorf("parseFakeQuota(%q): expected ok=false", in)
		}
	}
}

func TestParseFakeQuota_ScopedSegments(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	t.Run("single scoped entry", func(t *testing.T) {
		u, _, ok := parseFakeQuota("55,42,Fable:35", "", now)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if len(u.Limits) != 1 {
			t.Fatalf("Limits = %d, want 1", len(u.Limits))
		}
		l := u.Limits[0]
		if l.Kind != "weekly_scoped" || l.Group != "weekly" || l.Percent != 35 ||
			l.Severity != "normal" || !l.IsActive {
			t.Errorf("limit mismatch: %+v", l)
		}
		if l.Scope == nil || l.Scope.Model == nil || l.Scope.Model.DisplayName == nil ||
			*l.Scope.Model.DisplayName != "Fable" {
			t.Errorf("scope mismatch: %+v", l.Scope)
		}
		if l.ResetsAt == nil || !l.ResetsAt.After(now) {
			t.Errorf("ResetsAt: %v, want future", l.ResetsAt)
		}
	})

	t.Run("multiple scoped entries preserve order", func(t *testing.T) {
		u, _, ok := parseFakeQuota("55,42,Fable:35,Opus:80", "", now)
		if !ok || len(u.Limits) != 2 {
			t.Fatalf("ok=%v Limits=%d, want true/2", ok, len(u.Limits))
		}
		if *u.Limits[0].Scope.Model.DisplayName != "Fable" || *u.Limits[1].Scope.Model.DisplayName != "Opus" {
			t.Errorf("order: %+v", u.Limits)
		}
	})

	t.Run("no scoped segments leaves Limits nil", func(t *testing.T) {
		u, _, ok := parseFakeQuota("55,42", "", now)
		if !ok || u.Limits != nil {
			t.Errorf("ok=%v Limits=%+v, want true/nil", ok, u.Limits)
		}
	})

	t.Run("malformed scoped segment rejects whole var", func(t *testing.T) {
		for _, in := range []string{
			"55,42,Fable",     // no colon
			"55,42,:35",       // empty name
			"55,42,Fable:",    // empty pct
			"55,42,Fable:x",   // non-numeric pct
			"55,42,Fable:-1",  // below range
			"55,42,Fable:101", // above range
		} {
			if _, _, ok := parseFakeQuota(in, "", now); ok {
				t.Errorf("parseFakeQuota(%q): expected ok=false", in)
			}
		}
	})
}
