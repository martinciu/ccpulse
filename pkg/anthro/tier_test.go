package anthro

import "testing"

func TestTierSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"default_claude_max_20x", "max_20x"},
		{"default_claude_max_5x", "max_5x"},
		{"default_claude_pro", "pro"},
		{"default_claude_max_40x", "max_40x"}, // forward-compat
		{"", "unknown"},
		{"weird_value", "weird_value"},
	}
	for _, c := range cases {
		if got := TierSlug(c.in); got != c.want {
			t.Errorf("TierSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTierPretty(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"default_claude_max_20x", "Max 20x"},
		{"default_claude_max_5x", "Max 5x"},
		{"default_claude_pro", "Pro"},
		{"default_claude_max_40x", "Max 40x"},
		{"", "Unknown"},
	}
	for _, c := range cases {
		if got := TierPretty(c.in); got != c.want {
			t.Errorf("TierPretty(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
