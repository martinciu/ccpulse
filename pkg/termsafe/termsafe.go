// Package termsafe strips non-printable runes from strings that reach the
// terminal but did not come from ccpulse itself.
//
// ccpulse renders several classes of untrusted display string: model ids
// read verbatim from on-disk JSONL (messages.model, with no validation —
// CCPULSE_PROJECTS_ROOT can point anywhere), project paths taken from a
// message's cwd, and display_name values sourced from the Anthropic usage
// API (attacker-controlled the moment that response is MitM'd or spoofed).
// lipgloss treats ANSI escape sequences as zero-width when it computes
// Width/MaxWidth, so they pass straight through layout math and land in the
// painted frame unless stripped first — an unterminated SGR could recolor
// every row below a box, a bare BEL could ring the terminal, and a bare \r
// could overwrite a border.
package termsafe

import (
	"strings"
	"unicode"
)

// Printable strips non-printable runes from s. It is a strings.Map over
// unicode.IsPrint, so it removes C0/C1 control characters (including \n,
// \r, \x1b, DEL, and single-byte C1 codes like U+009B) and other
// non-printable categories such as bidi overrides (U+202E) and zero-width
// format characters, while preserving spaces. Combining marks pass through
// unchanged — unicode.IsPrint treats them as printable — which is a known,
// accepted gap: this function guards against terminal-escape injection,
// not against every way a string can render oddly.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
}
