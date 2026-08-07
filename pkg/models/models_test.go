package models

import "testing"

// The raw ids below are every distinct value present in a real ~/.cache/ccpulse
// state.db, plus two shapes that decide the fallback path and the empty bucket.
func TestCanonical(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"undated claude id unchanged", "claude-opus-4-7", "claude-opus-4-7"},
		{"single-segment version unchanged", "claude-opus-5", "claude-opus-5"},
		{"dated haiku folded", "claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"dated opus folded", "claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"dated sonnet folded", "claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"third-party date folded too", "minimax/minimax-m2.7-20260318", "minimax/minimax-m2.7"},
		{"old id order still folds date", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"colon id untouched", "gpt-oss:20b", "gpt-oss:20b"},
		{"slug id untouched", "openai/gpt-oss-120b:free", "openai/gpt-oss-120b:free"},
		{"short numeric tail is not a date", "qwen/qwen3-235b-a22b-04-28", "qwen/qwen3-235b-a22b-04-28"},
		{"sentinel untouched", "<synthetic>", "<synthetic>"},
		{"empty", "", ""},
		{"leading-dash date returns verbatim", "-20250101", "-20250101"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Canonical(tt.id); got != tt.want {
				t.Errorf("Canonical(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"opus 4.7", "claude-opus-4-7", "Opus 4.7"},
		{"opus 4.8", "claude-opus-4-8", "Opus 4.8"},
		{"opus 4.6", "claude-opus-4-6", "Opus 4.6"},
		{"opus 5", "claude-opus-5", "Opus 5"},
		{"fable 5", "claude-fable-5", "Fable 5"},
		{"sonnet 4.6", "claude-sonnet-4-6", "Sonnet 4.6"},
		{"sonnet 5", "claude-sonnet-5", "Sonnet 5"},
		{"dated haiku labels as canonical", "claude-haiku-4-5-20251001", "Haiku 4.5"},
		{"dated opus labels as canonical", "claude-opus-4-5-20251101", "Opus 4.5"},
		{"dated sonnet labels as canonical", "claude-sonnet-4-5-20250929", "Sonnet 4.5"},
		{"third-party verbatim after fold", "minimax/minimax-m2.7-20260318", "minimax/minimax-m2.7"},
		{"colon id verbatim", "gpt-oss:20b", "gpt-oss:20b"},
		{"slug id verbatim", "openai/gpt-oss-120b:free", "openai/gpt-oss-120b:free"},
		{"qwen verbatim", "qwen/qwen3-235b-a22b-04-28", "qwen/qwen3-235b-a22b-04-28"},
		{"sentinel verbatim", "<synthetic>", "<synthetic>"},
		{"old id order falls back, never mislabels", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"non-numeric version falls back", "claude-opus-4-latest", "claude-opus-4-latest"},
		{"empty is the unknown bucket", "", "(unknown model)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Label(tt.id); got != tt.want {
				t.Errorf("Label(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

// TestLabel_IdempotentOverCanonical pins the contract that callers cannot get
// the order wrong: Label canonicalizes its own input.
func TestLabel_IdempotentOverCanonical(t *testing.T) {
	for _, id := range []string{
		"claude-haiku-4-5-20251001", "claude-opus-4-7", "<synthetic>",
		"minimax/minimax-m2.7-20260318", "",
	} {
		if got, want := Label(id), Label(Canonical(id)); got != want {
			t.Errorf("Label(%q) = %q, want Label(Canonical(%q)) = %q", id, got, id, want)
		}
	}
}

// TestLabel_NeverEmptyForNonEmptyInput guards the fallback's totality: an id
// shaped unlike anything anticipated must still render something readable.
func TestLabel_NeverEmptyForNonEmptyInput(t *testing.T) {
	for _, id := range []string{"x", "-", "claude-", "claude--1", "----", "a-b-c-d-e", "-20250101"} {
		if Label(id) == "" {
			t.Errorf("Label(%q) = \"\", want non-empty", id)
		}
	}
}

// TestCanonical_NeverEmptyForNonEmptyInput guards Canonical's totality
// directly: the fold must never produce the empty string — pkg/cache treats
// "" as the unknown-model sentinel, so an empty result silently reclassifies
// a real id. "-20250101" is the shape that used to do exactly that (#479).
func TestCanonical_NeverEmptyForNonEmptyInput(t *testing.T) {
	for _, id := range []string{"-20250101", "x", "-", "claude-", "claude--1", "----", "a-b-c-d-e"} {
		if Canonical(id) == "" {
			t.Errorf("Canonical(%q) = \"\", want non-empty", id)
		}
	}
}
