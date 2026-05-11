package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/status"
)

func TestFormatReset7d_Content(t *testing.T) {
	// formatReset7d is a pure variable-width formatter — no padding.
	// Layout (right-align inside a fixed slot) is the renderQuotaSide
	// helper's job. Asserts raw equality so accidental padding regressions
	// fail loudly. Boundary cases: 0, 60 (hour rollover), 1439 (just before
	// day mode), 1440 (just at), 10080 (7 days).
	tests := []struct {
		mins int
		want string
	}{
		{0, "00:00"},
		{30, "00:30"},
		{60, "01:00"},
		{90, "01:30"},
		{1439, "23:59"},
		{1440, "1d"},
		{1500, "1d"}, // truncates, does not round
		{10080, "7d"},
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
	// while sub-1 rates keep their fractional digit.
	tests := []struct {
		slope float64
		want  string
	}{
		{0, "0%/h"},
		{0.4, "0.4%/h"},
		{1.0, "1%/h"},
		{12.0, "12%/h"},
		{12.5, "12.5%/h"},
		{23.0, "23%/h"},
		{100.0, "100%/h"},
		{105.7, "105.7%/h"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%g", tt.slope), func(t *testing.T) {
			got := formatBurnRate(tt.slope)
			if got != tt.want {
				t.Errorf("formatBurnRate(%g) = %q, want %q", tt.slope, got, tt.want)
			}
		})
	}
}

func TestSeverityFor(t *testing.T) {
	// severityFor exhaustively classifies a *status.Projection into one of
	// five visual states. Order matters: nil short-circuits before any
	// field is read; Confidence="low" short-circuits before WillOverreach
	// is read; then the three projection-based states fan out. The 30-min
	// imminent boundary is the only threshold this function owns.
	min9 := 9
	min41 := 41
	min30 := 30
	tests := []struct {
		name string
		p    *status.Projection
		want burnSeverity
	}{
		{
			name: "nil projection → noData",
			p:    nil,
			want: burnSeverityNoData,
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
			want: burnSeverityWarmingUp,
		},
		{
			name: "no overreach → safe",
			p: &status.Projection{
				SlopePctPerHour:     12,
				ProjectedPctAtReset: 54,
				WillOverreach:       false,
				Confidence:          "ok",
			},
			want: burnSeveritySafe,
		},
		{
			name: "overreach + eta > 30m → watch",
			p: &status.Projection{
				SlopePctPerHour:     23,
				ProjectedPctAtReset: 117,
				WillOverreach:       true,
				MinutesTo100Pct:     &min41,
				Confidence:          "ok",
			},
			want: burnSeverityWatch,
		},
		{
			name: "overreach + eta == 30m → danger (boundary)",
			p: &status.Projection{
				SlopePctPerHour:     20,
				ProjectedPctAtReset: 115,
				WillOverreach:       true,
				MinutesTo100Pct:     &min30,
				Confidence:          "ok",
			},
			want: burnSeverityDanger,
		},
		{
			name: "overreach + eta < 30m → danger",
			p: &status.Projection{
				SlopePctPerHour:     45,
				ProjectedPctAtReset: 200,
				WillOverreach:       true,
				MinutesTo100Pct:     &min9,
				Confidence:          "ok",
			},
			want: burnSeverityDanger,
		},
		{
			name: "overreach + MinutesTo100Pct nil (already at limit) → danger",
			p: &status.Projection{
				SlopePctPerHour:     100,
				ProjectedPctAtReset: 500,
				WillOverreach:       true,
				MinutesTo100Pct:     nil,
				Confidence:          "ok",
			},
			want: burnSeverityDanger,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := severityFor(tt.p)
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
	const slotW = 60
	dim := lipgloss.NewStyle().Foreground(Base01)
	safe := lipgloss.NewStyle().Foreground(Green)
	watch := lipgloss.NewStyle().Foreground(Yellow)
	danger := lipgloss.NewStyle().Foreground(Red)

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
			notSubstrs:  []string{"%/h", "limit in", "projecting"},
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
			notSubstrs:  []string{"30%/h", "150", "limit in"},
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
			wantSubstrs: []string{"12%/h", "projecting 54%"},
			notSubstrs:  []string{"limit in", "already at limit"},
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
			wantSubstrs: []string{"23%/h", "projecting 117%", "limit in 41m"},
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
			wantSubstrs: []string{"45%/h", "projecting 200%", "limit in 9m"},
			wantStyle:   danger,
		},
		{
			name: "danger with nil eta degrades to 'already at limit'",
			p: &status.Projection{
				SlopePctPerHour:     100,
				ProjectedPctAtReset: 500,
				WillOverreach:       true,
				MinutesTo100Pct:     nil,
				Confidence:          "ok",
			},
			wantSubstrs: []string{"already at limit"},
			notSubstrs:  []string{"limit in"},
			wantStyle:   danger,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderBurnRateSide("5h ", tt.p, slotW)
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
			// Style probe: render a single-char marker through the expected
			// style and assert its escape envelope is present in the output.
			// Survives lipgloss version bumps because we don't hard-code
			// escape bytes — we compare what lipgloss itself produces today.
			marker := tt.wantStyle.Render("X")
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

// splitANSIEnvelope splits a lipgloss-styled single-character string
// "ESC[...mXESC[0m" into (open, close, true). Used to fingerprint the
// styling applied without hard-coding escape sequences.
func splitANSIEnvelope(styled string) (open, close string, ok bool) {
	idx := strings.IndexByte(styled, 'X')
	if idx <= 0 || idx >= len(styled)-1 {
		return "", "", false
	}
	return styled[:idx], styled[idx+1:], true
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
