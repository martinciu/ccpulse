package status

import (
	"math"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// ScopedLimit is the distilled projection of one weekly_scoped entry from
// the usage API's limits array — the per-model weekly quota (issue #463).
// Consumed by the TUI header (one row per entry) and serialized into
// `status --json` as `scoped_limits`. Kind is kept even though the
// distillation currently filters to weekly_scoped, so a future widening
// doesn't have to change the JSON shape.
type ScopedLimit struct {
	Kind           string `json:"kind"`
	Model          string `json:"model"`
	Percent        int    `json:"percent"`
	IsActive       bool   `json:"is_active"`
	Severity       string `json:"severity"`
	MinutesToReset *int   `json:"minutes_to_reset"`
}

// distillScopedLimits filters u.Limits down to labelled weekly_scoped
// entries, preserving API order. Entries without a scope.model.display_name
// are skipped — there is nothing to label a row with (ID is null in every
// observed probe). Returns nil when u is nil or nothing matches, so
// Window.ScopedLimits serializes via omitempty exactly like the other
// optional Window fields. Percent gets the same round+clamp as Percent7d;
// MinutesToReset follows the max(0, resets_at−now) convention.
func distillScopedLimits(u *anthro.Usage, now time.Time) []ScopedLimit {
	if u == nil {
		return nil
	}
	var out []ScopedLimit
	for _, l := range u.Limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil ||
			l.Scope.Model.DisplayName == nil || *l.Scope.Model.DisplayName == "" {
			continue
		}
		sl := ScopedLimit{
			Kind:     l.Kind,
			Model:    *l.Scope.Model.DisplayName,
			Percent:  clampPct(int(math.Round(l.Percent))),
			IsActive: l.IsActive,
			Severity: l.Severity,
		}
		if l.ResetsAt != nil {
			mins := max(int(l.ResetsAt.Sub(now).Minutes()), 0)
			sl.MinutesToReset = &mins
		}
		out = append(out, sl)
	}
	return out
}
