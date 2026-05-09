package tui

import (
	"strings"
	"testing"
)

func TestInitialView(t *testing.T) {
	m := New(Deps{})
	v := m.View()
	if !strings.Contains(v, "ccpulse") {
		t.Errorf("expected 'ccpulse' in view, got:\n%s", v)
	}
	if !strings.Contains(v, "Live") {
		t.Errorf("expected Live tab in view")
	}
}
