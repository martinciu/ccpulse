package termsafe

import "testing"

// C1 CSI, DEL, and RIGHT-TO-LEFT OVERRIDE are built from their code points
// rather than embedded as literal characters in source — keeps the file
// free of raw control/bidi bytes while still exercising the exact runes
// #475.35 called out.
const (
	c1CSI   = rune(0x9b)
	del     = rune(0x7f)
	bidiRLO = rune(0x202e)
)

// TestPrintable_StripsControlChars covers both the C0 range (the original
// pin for #475.10) and the wider set of C1, DEL, and bidi-override runes
// that a narrower implementation such as `if r < 0x20 { return -1 }` would
// let through (#475.35). unicode.IsPrint strips all of them.
func TestPrintable_StripsControlChars(t *testing.T) {
	cases := []struct {
		name  string
		label string
		bad   rune
	}{
		{"ansi_color", "claude-\x1b[31mopus\x1b[0m-4-5", '\x1b'},
		{"osc_bell", "\x1b]0;pwned\aclaude-opus-4-5", '\a'},
		{"carriage_return", "claude-opus\r4-5", '\r'},
		{"line_feed", "claude-opus\n4-5", '\n'},
		{"c1_csi", "claude-" + string(c1CSI) + "31mopus", c1CSI},
		{"del", "claude-opus" + string(del) + "4-5", del},
		{"bidi_override", "claude-opus" + string(bidiRLO) + "4-5", bidiRLO},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Printable(tc.label)
			for _, r := range got {
				if r == tc.bad {
					t.Errorf("Printable(%q) = %q, retained control rune %U", tc.label, got, tc.bad)
				}
			}
		})
	}
}

// TestPrintable_PreservesOrdinaryText pins that everyday labels — including
// spaces, which unicode.IsPrint deliberately keeps — round-trip unchanged.
func TestPrintable_PreservesOrdinaryText(t *testing.T) {
	cases := []string{
		"claude-opus-4-5",
		"my project (worktree)",
		"  leading and trailing  ",
	}
	for _, s := range cases {
		if got := Printable(s); got != s {
			t.Errorf("Printable(%q) = %q, want unchanged", s, got)
		}
	}
}

// TestPrintable_ControlOnlyStringBecomesEmpty pins that a label made
// entirely of control bytes reduces to "" rather than surviving as
// whitespace-like content — callers (e.g. distillScopedLimits) rely on this
// to decide whether to skip the entry entirely.
func TestPrintable_ControlOnlyStringBecomesEmpty(t *testing.T) {
	if got := Printable("\x1b\n\r\x07"); got != "" {
		t.Errorf("Printable(all-control) = %q, want empty", got)
	}
}
