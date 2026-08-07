// Package models derives stable identities and display names for the model ids
// recorded in transcripts. It is the sibling of pkg/projects: where that turns
// a repo root into a project label, this turns a raw API model id into a
// canonical id and a human label.
//
// Display names are DERIVED, not looked up in a table, so they cannot go stale
// when a new model ships. The derivation is deliberately narrow and the
// fallback total: an id shaped unlike today's convention renders verbatim —
// readable and correct — rather than being guessed at.
package models

import "strings"

// unknownLabel is the display name for the empty-model bucket, mirroring
// pkg/projects' "(no project)".
const unknownLabel = "(unknown model)"

// Canonical folds a raw transcript model id to its release-independent form by
// stripping a trailing -YYYYMMDD release-date segment, so dated and undated
// variants of the same model (claude-haiku-4-5-20251001 and claude-haiku-4-5)
// aggregate as one. Ids without such a segment — third-party ids, sentinels
// like <synthetic>, the empty string — are returned unchanged, as is an id
// that is nothing but a dash and a date ("-20250101"), where stripping would
// leave the empty string that pkg/cache reserves for its unknown-model bucket.
func Canonical(id string) string {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 || !isReleaseDate(id[i+1:]) {
		return id
	}
	return id[:i]
}

// Label returns a human display name for a model id. It canonicalizes its
// input first, so Label(raw) == Label(Canonical(raw)) and callers cannot get
// the order wrong.
//
// An id renders as "Family Major.Minor" only when it is unambiguously a modern
// Claude id: at least three segments, a literal "claude" head, a non-numeric
// family, and numeric version segments throughout. Everything else returns the
// canonical id verbatim — including the pre-2025 id order (claude-3-5-sonnet),
// where guessing a family from the wrong position would be confidently wrong.
func Label(id string) string {
	c := Canonical(id)
	if c == "" {
		return unknownLabel
	}
	parts := strings.Split(c, "-")
	if len(parts) < 3 || parts[0] != "claude" || parts[1] == "" || isNumeric(parts[1]) {
		return c
	}
	for _, p := range parts[2:] {
		if !isNumeric(p) {
			return c
		}
	}
	return upperFirst(parts[1]) + " " + strings.Join(parts[2:], ".")
}

// isReleaseDate reports whether s is exactly eight digits (a YYYYMMDD segment).
func isReleaseDate(s string) bool {
	return len(s) == 8 && isNumeric(s)
}

// isNumeric reports whether s is non-empty and all ASCII digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// upperFirst uppercases an ASCII leading letter. Model families are ASCII by
// construction, so no unicode casing is needed.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
