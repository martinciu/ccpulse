package tui

import (
	"os"
	"strings"
)

// dateOrder picks between MM/DD and DD/MM in the 5-char date slot of
// formatXLabel / dateLabel. The 5-char budget means YYYY-MM-DD never
// fits, so even ISO-preferring locales collapse to one of these two.
type dateOrder int

const (
	// dateOrderDayFirst renders DD/MM — used for every region except US.
	dateOrderDayFirst dateOrder = iota
	// dateOrderMonthFirst renders MM/DD — used only for region US.
	// Also the fail-closed default when locale env is unset or
	// unparseable, matching the legacy hard-coded en_US behavior.
	dateOrderMonthFirst
)

// extractRegion walks a locale tag and returns the first segment that
// is exactly two uppercase ASCII letters. Handles both POSIX form
// ("en_US") and BCP-47 form ("en-US", "zh-Hans-CN"). Returns "" when
// no such segment exists (e.g. bare "en", empty string, or garbage).
func extractRegion(v string) string {
	for _, seg := range strings.FieldsFunc(v, func(r rune) bool {
		return r == '_' || r == '-'
	}) {
		if len(seg) == 2 && isAllUpperASCII(seg) {
			return seg
		}
	}
	return ""
}

func isAllUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// parseLocaleEnv extracts a region from a single locale env value
// (POSIX or BCP-47 form) and applies the Just-US policy:
//
//	"en_US.UTF-8"       → US      → MonthFirst
//	"de_DE@euro"        → DE      → DayFirst
//	"sr_RS.UTF-8@latin" → RS      → DayFirst
//	"zh-Hans-CN"        → CN      → DayFirst
//	"C" / "POSIX" / ""  → (none)  → MonthFirst (fail-closed to en_US)
//	"en" / "garbage"    → (none)  → MonthFirst (no region extractable)
//
// The fail-closed default keeps existing US users with default LANG
// unchanged.
func parseLocaleEnv(v string) dateOrder {
	// Strip ".encoding" and "@modifier" suffixes.
	if i := strings.IndexAny(v, ".@"); i >= 0 {
		v = v[:i]
	}
	if v == "" || v == "C" || v == "POSIX" {
		return dateOrderMonthFirst
	}
	region := extractRegion(v)
	// No extractable region (bare "en", "garbage", etc.) → fail-closed
	// to MonthFirst, the legacy en_US convention. Same for region "US".
	if region == "" || region == "US" {
		return dateOrderMonthFirst
	}
	return dateOrderDayFirst
}

// detectDateOrder reads LC_TIME, LC_ALL, LANG (first non-empty wins)
// and returns the order for the user's region. All-empty / unset env
// returns dateOrderMonthFirst, matching the legacy hard-coded en_US
// behavior — existing US users on default macOS / Linux see no change.
//
// No shell-out: matches ccpulse's "no runtime git" principle and
// avoids the os/exec dependency that a `defaults read -g AppleLocale`
// path would add. macOS Terminal/iTerm propagate the System Settings
// region into LANG by default, so env-only covers the typical case.
func detectDateOrder() dateOrder {
	for _, key := range []string{"LC_TIME", "LC_ALL", "LANG"} {
		if v := os.Getenv(key); v != "" {
			return parseLocaleEnv(v)
		}
	}
	return dateOrderMonthFirst
}
