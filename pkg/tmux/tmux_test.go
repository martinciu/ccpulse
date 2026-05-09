package tmux

import (
	"fmt"
	"strings"
	"testing"
)

func TestPanePathsParses(t *testing.T) {
	c := New()
	c.runner = func(args ...string) (string, error) {
		return "/Users/x/code/dotfiles\n/Users/x/code/foo\n", nil
	}
	got, err := c.PanePaths("session-x")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Users/x/code/dotfiles", "/Users/x/code/foo"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCurrentSessionParses(t *testing.T) {
	c := New()
	c.runner = func(args ...string) (string, error) { return "main\n", nil }
	got, err := c.CurrentSession()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != "main" {
		t.Errorf("got %q", got)
	}
}
