package main

import "testing"

// backgroundQueryable gates the OSC background query so the label fade only
// targets the real terminal background when it can be detected reliably —
// otherwise termenv's fall-back-to-black would reintroduce the fade-to-black
// smudge. These cases lock that gate.
func TestBackgroundQueryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		charDevice bool
		term       string
		want       bool
	}{
		{"real xterm tty", true, "xterm-256color", true},
		{"real ghostty tty", true, "xterm-ghostty", true},
		{"plain xterm", true, "xterm", true},
		{"not a tty (piped/test)", false, "xterm-256color", false},
		{"tmux multiplexer", true, "tmux-256color", false},
		{"screen multiplexer", true, "screen.xterm-256color", false},
		{"dumb terminal", true, "dumb", false},
		{"tty but empty TERM", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := backgroundQueryable(tc.charDevice, tc.term); got != tc.want {
				t.Errorf("backgroundQueryable(%v, %q) = %v, want %v", tc.charDevice, tc.term, got, tc.want)
			}
		})
	}
}
