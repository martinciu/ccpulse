package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/status"
)

func TestFormatReset7d_Content(t *testing.T) {
	// formatReset7d is a pure variable-width formatter — no padding —
	// that delegates to durString so the 7d side reads like the 5h side
	// ("5d 12h", "18h 34m", "34m"). Layout (right-align inside a fixed
	// slot) is the renderQuotaSide helper's job. Asserts raw equality so
	// accidental padding regressions fail loudly. Boundary cases: 0, 60
	// (hour rollover), 1439 (just before day mode), 1440 (just at), 10080
	// (7 days).
	tests := []struct {
		mins int
		want string
	}{
		{0, "0m"},
		{30, "30m"},
		{60, "1h 0m"},
		{90, "1h 30m"},
		{1439, "23h 59m"},
		{1440, "1d 0h"},
		{1500, "1d 1h"},
		{10080, "7d 0h"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dmins", tt.mins), func(t *testing.T) {
			got := formatReset7d(tt.mins)
			if got != tt.want {
				t.Errorf("formatReset7d(%d) = %q, want %q", tt.mins, got, tt.want)
			}
		})
	}
}

func TestDurString(t *testing.T) {
	// durString formats minute counts as "Xm" (< 60), "Xh Ym" (60-1439), or
	// "Xd Yh" (>= 1440). The day branch is dormant for the existing
	// MinutesToReset caller (5h cap) but is exercised by 7d burn-rate
	// ETAs that can exceed multiple days.
	tests := []struct {
		mins int
		want string
	}{
		{0, "0m"},
		{30, "30m"},
		{59, "59m"},
		{60, "1h 0m"},
		{90, "1h 30m"},
		{299, "4h 59m"},
		{1439, "23h 59m"},
		{1440, "1d 0h"},
		{1500, "1d 1h"},
		{4500, "3d 3h"},
		{10080, "7d 0h"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dmins", tt.mins), func(t *testing.T) {
			got := durString(tt.mins)
			if got != tt.want {
				t.Errorf("durString(%d) = %q, want %q", tt.mins, got, tt.want)
			}
		})
	}
}

func TestFormatBurnRate(t *testing.T) {
	// formatBurnRate is the slope formatter for the burn-rate row.
	// Rule: %.1f then strip trailing ".0" so integer rates read clean
	// while sub-1 rates keep their fractional digit. The unit decides
	// whether the input slope is interpreted as %/h directly (5h side)
	// or scaled by 24 for %/day (7d side).
	tests := []struct {
		name  string
		slope float64
		unit  burnRateUnit
		want  string
	}{
		// per-hour cases — preserve existing behaviour exactly.
		{"per-hour zero", 0, burnRateUnitPerHour, "0%/h"},
		{"per-hour fractional", 0.4, burnRateUnitPerHour, "0.4%/h"},
		{"per-hour integer", 1.0, burnRateUnitPerHour, "1%/h"},
		{"per-hour twelve", 12.0, burnRateUnitPerHour, "12%/h"},
		{"per-hour 12.5", 12.5, burnRateUnitPerHour, "12.5%/h"},
		{"per-hour twenty-three", 23.0, burnRateUnitPerHour, "23%/h"},
		{"per-hour hundred", 100.0, burnRateUnitPerHour, "100%/h"},
		{"per-hour 105.7", 105.7, burnRateUnitPerHour, "105.7%/h"},

		// per-day cases — slope is multiplied by 24 before formatting.
		{"per-day zero", 0, burnRateUnitPerDay, "0%/day"},
		{"per-day sustainable", 0.5, burnRateUnitPerDay, "12%/day"}, // 0.5 * 24 = 12.0 -> "12"
		{"per-day on-track", 0.6, burnRateUnitPerDay, "14.4%/day"},  // 0.6 * 24 = 14.4
		{"per-day overreach", 1.0, burnRateUnitPerDay, "24%/day"},   // 1 * 24
		{"per-day hot", 5.0, burnRateUnitPerDay, "120%/day"},
		{"per-day rounds to integer", 0.04166667, burnRateUnitPerDay, "1%/day"}, // 0.04166667 * 24 = 1.0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBurnRate(tt.slope, tt.unit)
			if got != tt.want {
				t.Errorf("formatBurnRate(%g, %v) = %q, want %q", tt.slope, tt.unit, got, tt.want)
			}
		})
	}
}

func TestSeverityFor(t *testing.T) {
	// severityFor exhaustively classifies a *status.Projection into one of
	// five visual states. Order matters: nil short-circuits before any
	// field is read; Confidence="low" short-circuits before WillOverreach
	// is read; then the three projection-based states fan out. The
	// imminent boundary is 10% of the bucket's window — 30 min for 5h,
	// 1008 min (~16.8h) for 7d.
	min9 := 9
	min41 := 41
	min30 := 30
	min1007 := 1007
	min1008 := 1008
	min1100 := 1100
	tests := []struct {
		name   string
		p      *status.Projection
		window time.Duration
		want   burnSeverity
	}{
		{
			name:   "nil projection → noData",
			p:      nil,
			window: 5 * time.Hour,
			want:   burnSeverityNoData,
		},
		{
			name: "low confidence → warmingUp (overrides projection)",
			p: &status.Projection{
				SlopePctPerHour:     30,
				ProjectedPctAtReset: 150,
				WillOverreach:       true,
				MinutesTo100Pct:     &min9,
				Confidence:          "low",
			},
			window: 5 * time.Hour,
			want:   burnSeverityWarmingUp,
		},
		{
			name: "no overreach → safe",
			p: &status.Projection{
				SlopePctPerHour:     12,
				ProjectedPctAtReset: 54,
				WillOverreach:       false,
				Confidence:          "ok",
			},
			window: 5 * time.Hour,
			want:   burnSeveritySafe,
		},
		{
			// First half of the contract pin: only the literal "low"
			// string triggers warming-up. With !WillOverreach this would
			// pass even if "" matched "low" (the safe branch short-
			// circuits earlier) — paired with the next case to fully
			// prove the fall-through.
			name: "empty Confidence (zero value) + no overreach → safe (not warming up)",
			p: &status.Projection{
				SlopePctPerHour:     12,
				ProjectedPctAtReset: 54,
				WillOverreach:       false,
				Confidence:          "",
			},
			window: 5 * time.Hour,
			want:   burnSeveritySafe,
		},
		{
			// Second half: forces dispatch through the Confidence=="low"
			// check (WillOverreach=true defeats the safe short-circuit).
			// If "" were treated as "low", this would return warmingUp;
			// expecting watch proves "" falls through to projection-based
			// classification as intended.
			name: "empty Confidence + overreach + eta > threshold → watch (proves fall-through past low check)",
			p: &status.Projection{
				SlopePctPerHour:     23,
				ProjectedPctAtReset: 117,
				WillOverreach:       true,
				MinutesTo100Pct:     &min41,
				Confidence:          "",
			},
			window: 5 * time.Hour,
			want:   burnSeverityWatch,
		},
		{
			name: "5h overreach + eta > 30m → watch",
			p: &status.Projection{
				SlopePctPerHour:     23,
				ProjectedPctAtReset: 117,
				WillOverreach:       true,
				MinutesTo100Pct:     &min41,
				Confidence:          "ok",
			},
			window: 5 * time.Hour,
			want:   burnSeverityWatch,
		},
		{
			name: "5h overreach + eta == 30m → danger (boundary)",
			p: &status.Projection{
				SlopePctPerHour:     20,
				ProjectedPctAtReset: 115,
				WillOverreach:       true,
				MinutesTo100Pct:     &min30,
				Confidence:          "ok",
			},
			window: 5 * time.Hour,
			want:   burnSeverityDanger,
		},
		{
			name: "5h overreach + eta < 30m → danger",
			p: &status.Projection{
				SlopePctPerHour:     45,
				ProjectedPctAtReset: 200,
				WillOverreach:       true,
				MinutesTo100Pct:     &min9,
				Confidence:          "ok",
			},
			window: 5 * time.Hour,
			want:   burnSeverityDanger,
		},
		{
			name: "5h overreach + MinutesTo100Pct nil (already at limit) → danger",
			p: &status.Projection{
				SlopePctPerHour:     100,
				ProjectedPctAtReset: 500,
				WillOverreach:       true,
				MinutesTo100Pct:     nil,
				Confidence:          "ok",
			},
			window: 5 * time.Hour,
			want:   burnSeverityDanger,
		},
		{
			// 7d threshold = 10% of 10080 min = 1008 min. eta=1100 is
			// above the threshold so still "watch", even though it would
			// be deep into "danger" under the old fixed-30-min rule.
			name: "7d overreach + eta > 1008m (10% of 7d) → watch",
			p: &status.Projection{
				SlopePctPerHour:     0.5,
				ProjectedPctAtReset: 105,
				WillOverreach:       true,
				MinutesTo100Pct:     &min1100,
				Confidence:          "ok",
			},
			window: 7 * 24 * time.Hour,
			want:   burnSeverityWatch,
		},
		{
			name: "7d overreach + eta == 1008m → danger (boundary)",
			p: &status.Projection{
				SlopePctPerHour:     0.5,
				ProjectedPctAtReset: 110,
				WillOverreach:       true,
				MinutesTo100Pct:     &min1008,
				Confidence:          "ok",
			},
			window: 7 * 24 * time.Hour,
			want:   burnSeverityDanger,
		},
		{
			name: "7d overreach + eta < 1008m → danger",
			p: &status.Projection{
				SlopePctPerHour:     0.7,
				ProjectedPctAtReset: 130,
				WillOverreach:       true,
				MinutesTo100Pct:     &min1007,
				Confidence:          "ok",
			},
			window: 7 * 24 * time.Hour,
			want:   burnSeverityDanger,
		},
		{
			// Cross-bucket sanity, 7d half: this is the same projection
			// (eta=41m) that the "5h overreach + eta > 30m → watch" case
			// above classifies as watch on the 5h window. Here, with the
			// 7d window (threshold = 1008 min), eta=41m is well below
			// the threshold and classifies as danger. Demonstrates the
			// 10%-of-window scaling.
			name: "7d overreach + eta=41m (well below 1008m) → danger",
			p: &status.Projection{
				SlopePctPerHour:     23,
				ProjectedPctAtReset: 117,
				WillOverreach:       true,
				MinutesTo100Pct:     &min41,
				Confidence:          "ok",
			},
			window: 7 * 24 * time.Hour,
			want:   burnSeverityDanger,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := severityFor(tt.p, tt.window)
			if got != tt.want {
				t.Errorf("severityFor: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRenderBurnRateSide(t *testing.T) {
	// renderBurnRateSide builds the per-side burn-rate string for the
	// header's second row. Tests assert substring content and that the
	// chosen lipgloss style was applied (verified by rendering a known
	// marker through the expected style and checking the marker's escape
	// envelope appears in the output).
	withForcedColor(t)
	withForcedDarkBackground(t, true)
	const slotW = 60
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	safe := lipgloss.NewStyle().Foreground(colorSafe)
	watch := lipgloss.NewStyle().Foreground(colorWatch)
	danger := lipgloss.NewStyle().Foreground(colorDanger)

	min9 := 9
	min41 := 41
	tests := []struct {
		name        string
		p           *status.Projection
		wantSubstrs []string // all must appear
		notSubstrs  []string // none may appear
		wantStyle   lipgloss.Style
	}{
		{
			name:        "nil projection renders dim no-data",
			p:           nil,
			wantSubstrs: []string{"(no data)"},
			notSubstrs:  []string{"%/h", "→", "·"},
			wantStyle:   dim,
		},
		{
			name: "warming up dims and hides numbers",
			p: &status.Projection{
				SlopePctPerHour:     30,
				ProjectedPctAtReset: 150,
				WillOverreach:       true,
				Confidence:          "low",
			},
			wantSubstrs: []string{"warming up"},
			notSubstrs:  []string{"30%/h", "150", "→", "·"},
			wantStyle:   dim,
		},
		{
			name: "safe shows rate + projection in green, no limit-in",
			p: &status.Projection{
				SlopePctPerHour:     12,
				ProjectedPctAtReset: 54,
				WillOverreach:       false,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"12%/h", "→54%"},
			notSubstrs:  []string{"·", "limit", "projecting"},
			wantStyle:   safe,
		},
		{
			name: "watch shows limit-in in yellow",
			p: &status.Projection{
				SlopePctPerHour:     23,
				ProjectedPctAtReset: 117,
				WillOverreach:       true,
				MinutesTo100Pct:     &min41,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"23%/h", "→117%", "·41m"},
			notSubstrs:  []string{"projecting", "limit in"},
			wantStyle:   watch,
		},
		{
			name: "danger shows limit-in in red",
			p: &status.Projection{
				SlopePctPerHour:     45,
				ProjectedPctAtReset: 200,
				WillOverreach:       true,
				MinutesTo100Pct:     &min9,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"45%/h", "→200%", "·9m"},
			notSubstrs:  []string{"projecting", "limit in"},
			wantStyle:   danger,
		},
		{
			name: "danger with nil eta degrades to 'at limit'",
			p: &status.Projection{
				SlopePctPerHour:     100,
				ProjectedPctAtReset: 500,
				WillOverreach:       true,
				MinutesTo100Pct:     nil,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"100%/h", "at limit"},
			notSubstrs:  []string{"→", "500", "projecting"},
			wantStyle:   danger,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderBurnRateSide("5h ", tt.p, slotW, 5*time.Hour, burnRateUnitPerHour)
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing substring %q\nfull output: %q", sub, got)
				}
			}
			for _, sub := range tt.notSubstrs {
				if strings.Contains(got, sub) {
					t.Errorf("output unexpectedly contains substring %q\nfull output: %q", sub, got)
				}
			}
			// Style probe: render a control-byte marker through the expected
			// style and assert its escape envelope is present in the output.
			// Survives lipgloss version bumps because we don't hard-code
			// escape bytes — we compare what lipgloss itself produces today.
			// Uses \x01 (SOH) rather than a printable char so the marker
			// can never collide with content elsewhere in the rendered text.
			marker := tt.wantStyle.Render(probeMarker)
			openSeq, closeSeq, ok := splitANSIEnvelope(marker)
			if !ok {
				t.Fatalf("could not split ANSI envelope from marker %q", marker)
			}
			if !strings.Contains(got, openSeq) || !strings.Contains(got, closeSeq) {
				t.Errorf("output missing expected style envelope (open=%q, close=%q)\nfull output: %q",
					openSeq, closeSeq, got)
			}
		})
	}
}

// probeMarker is the byte rendered through a lipgloss style to extract
// that style's open/close ANSI envelope for substring assertions in
// TestRenderBurnRateSide. Uses \x01 (SOH, a control byte) rather than
// a printable character so it can never collide with content elsewhere
// in the rendered output.
const probeMarker = "\x01"

// splitANSIEnvelope splits a lipgloss-styled single-character string
// "ESC[...m<probeMarker>ESC[0m" into (open, close, true). Used to
// fingerprint the styling applied without hard-coding escape sequences.
func splitANSIEnvelope(styled string) (open, close string, ok bool) {
	idx := strings.IndexByte(styled, probeMarker[0])
	if idx <= 0 || idx >= len(styled)-1 {
		return "", "", false
	}
	return styled[:idx], styled[idx+1:], true
}

func TestRenderBurnRateSide_PerDay(t *testing.T) {
	// The 7d side uses burnRateUnitPerDay so the rate token reads as
	// "%/day" with the slope scaled by 24. Everything else (severity
	// colour, projection/limit-in tail, layout) is identical to the
	// per-hour case covered by TestRenderBurnRateSide.
	withForcedColor(t)
	withForcedDarkBackground(t, true)
	const slotW = 60
	dim := lipgloss.NewStyle().Foreground(colorMuted)
	safe := lipgloss.NewStyle().Foreground(colorSafe)

	tests := []struct {
		name        string
		p           *status.Projection
		wantSubstrs []string
		notSubstrs  []string
		wantStyle   lipgloss.Style
	}{
		{
			name:        "nil projection still renders (no data), unit irrelevant",
			p:           nil,
			wantSubstrs: []string{"(no data)"},
			notSubstrs:  []string{"%/day", "%/h"},
			wantStyle:   dim,
		},
		{
			name: "warming-up renders dim text, no rate token",
			p: &status.Projection{
				SlopePctPerHour:     0.5,
				ProjectedPctAtReset: 60,
				Confidence:          "low",
			},
			wantSubstrs: []string{"warming up"},
			notSubstrs:  []string{"12%/day", "%/h"},
			wantStyle:   dim,
		},
		{
			name: "safe per-day scales slope by 24",
			p: &status.Projection{
				SlopePctPerHour:     0.5, // 0.5 * 24 = 12 → "12%/day"
				ProjectedPctAtReset: 60,
				WillOverreach:       false,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"12%/day", "→60%"},
			notSubstrs:  []string{"%/h", "·", "projecting"},
			wantStyle:   safe,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderBurnRateSide("7d ", tt.p, slotW, 7*24*time.Hour, burnRateUnitPerDay)
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing substring %q\nfull output: %q", sub, got)
				}
			}
			for _, sub := range tt.notSubstrs {
				if strings.Contains(got, sub) {
					t.Errorf("output unexpectedly contains substring %q\nfull output: %q", sub, got)
				}
			}
			marker := tt.wantStyle.Render(probeMarker)
			openSeq, closeSeq, ok := splitANSIEnvelope(marker)
			if !ok {
				t.Fatalf("could not split ANSI envelope from marker %q", marker)
			}
			if !strings.Contains(got, openSeq) || !strings.Contains(got, closeSeq) {
				t.Errorf("output missing expected style envelope (open=%q, close=%q)\nfull output: %q",
					openSeq, closeSeq, got)
			}
		})
	}
}

func TestRenderQuotaSide_ProducesExactSlotWidth(t *testing.T) {
	// renderQuotaSide's output width is determined entirely by its inputs:
	// lipgloss.Width(label) + bar.Width + statusBlockMaxW. Property under
	// test: the function returns exactly that width regardless of fill
	// ratio or reset-string width — short times get right-align pad inside
	// the fixed statusBlockMaxW slot, so the total stays constant.
	const labelStr = "5h "
	const barW = 10
	bar := progress.New(
		progress.WithWidth(barW),
		progress.WithoutPercentage(),
		progress.WithGradient(QuotaGradientStart, QuotaGradientEnd),
	)
	expectedW := lipgloss.Width(labelStr) + barW + 1 + statusBlockMaxW // +1 for barTimeGap
	cases := []struct {
		name      string
		fillRatio float64
		reset     string
	}{
		{"min", 0.0, "0m"},
		{"low_short_time", 0.05, "52m"},
		{"mid_short_time", 0.33, "5d"},
		{"mid_hhmm", 0.50, "12:34"},
		{"high_long_time", 0.95, "4h 59m"},
		{"max", 1.0, "4h 59m"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := renderQuotaSide(labelStr, bar, tt.fillRatio, tt.reset)
			if w := lipgloss.Width(got); w != expectedW {
				t.Errorf("renderQuotaSide fillRatio=%v reset=%q: width %d, want %d", tt.fillRatio, tt.reset, w, expectedW)
			}
		})
	}
}
