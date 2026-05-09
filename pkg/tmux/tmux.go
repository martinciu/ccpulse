// Package tmux is a thin wrapper around the tmux CLI for interrogating
// session/pane state. The exported runner field allows tests to swap in
// a mock without exec'ing real tmux.
package tmux

import (
	"os/exec"
	"strings"
)

type Client struct {
	runner func(args ...string) (string, error)
}

func New() *Client {
	return &Client{
		runner: func(args ...string) (string, error) {
			out, err := exec.Command("tmux", args...).Output()
			return string(out), err
		},
	}
}

// CurrentSession returns the current tmux session name as reported by
// `tmux display-message -p '#S'`. May trail a newline; caller trims.
func (c *Client) CurrentSession() (string, error) {
	return c.runner("display-message", "-p", "#S")
}

// PanePaths lists `pane_current_path` for every pane in the given
// tmux session.
func (c *Client) PanePaths(session string) ([]string, error) {
	out, err := c.runner("list-panes", "-t", session, "-F", "#{pane_current_path}")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
