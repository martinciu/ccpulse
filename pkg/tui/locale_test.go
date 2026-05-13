package tui

import "testing"

func TestExtractRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"POSIX form en_US", "en_US", "US"},
		{"BCP-47 form en-US", "en-US", "US"},
		{"BCP-47 with script subtag", "zh-Hans-CN", "CN"},
		{"polish posix", "pl_PL", "PL"},
		{"bare language no region", "en", ""},
		{"empty", "", ""},
		{"lowercase region rejected", "en_us", ""},
		{"three-letter region rejected", "en_USA", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractRegion(tt.in)
			if got != tt.want {
				t.Errorf("extractRegion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
