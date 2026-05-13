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

// detectDateOrder will be added in Task 2.
// parseLocaleEnv will be added in Task 2.
// Suppress unused-import lint until Task 2 lands.
var _ = os.Getenv
