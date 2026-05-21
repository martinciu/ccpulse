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
