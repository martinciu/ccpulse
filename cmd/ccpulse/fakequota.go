package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// Fixed reset offsets for the injected demo quota, chosen so the header's
// "resets in" slots and the burn-rate projection read realistically.
const (
	fakeQuota5hReset = 2*time.Hour + 10*time.Minute
	fakeQuota7dReset = 3 * 24 * time.Hour
)

// defaultFakeTier is the ceiling tier used when CCPULSE_FAKE_TIER is unset.
const defaultFakeTier = "max_20x"

// parseFakeQuota reads the demo/test fake-quota seam. quotaEnv is
// "<5h%>,<7d%>[,<model>:<weekly%>...]" (e.g. "55,42" or "55,42,Fable:35");
// each optional trailing segment injects one weekly_scoped limits entry
// (#463) so header scoped rows can be probed without a Fable-plan
// credential. tierEnv overrides the ceiling tier slug. It returns ok=false
// (never a fatal error) when quotaEnv is empty or any segment is
// malformed, so a missing/garbled var leaves the real quota path untouched.
func parseFakeQuota(quotaEnv, tierEnv string, now time.Time) (usage *anthro.Usage, tier string, ok bool) {
	quotaEnv = strings.TrimSpace(quotaEnv)
	if quotaEnv == "" {
		return nil, "", false
	}
	parts := strings.Split(quotaEnv, ",")
	if len(parts) < 2 {
		return nil, "", false
	}
	p5h, err5 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	p7d, err7 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err5 != nil || err7 != nil {
		return nil, "", false
	}
	if p5h < 0 || p5h > 100 || p7d < 0 || p7d > 100 {
		return nil, "", false
	}
	reset5h := now.Add(fakeQuota5hReset)
	reset7d := now.Add(fakeQuota7dReset)
	usage = &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: p5h, ResetsAt: &reset5h},
		SevenDay: &anthro.Bucket{Utilization: p7d, ResetsAt: &reset7d},
	}
	for _, seg := range parts[2:] {
		name, pctStr, found := strings.Cut(seg, ":")
		name = strings.TrimSpace(name)
		pct, errP := strconv.ParseFloat(strings.TrimSpace(pctStr), 64)
		if !found || name == "" || errP != nil || pct < 0 || pct > 100 {
			return nil, "", false
		}
		usage.Limits = append(usage.Limits, anthro.Limit{
			Kind: "weekly_scoped", Group: "weekly", Percent: pct,
			Severity: "normal", ResetsAt: &reset7d, IsActive: true,
			Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: &name}},
		})
	}
	tier = strings.TrimSpace(tierEnv)
	if tier == "" {
		tier = defaultFakeTier
	}
	return usage, tier, true
}
