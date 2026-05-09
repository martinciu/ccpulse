package status

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/config"
)

func TestJSONOutput(t *testing.T) {
	w := Window{
		Tokens5h:       1234567,
		Cost5hUSD:      4.21,
		MinutesToReset: 107,
		Percent:        61,
		CeilingLabel:   "max_20x",
	}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"percent":61`) {
		t.Errorf("missing percent in %s", out)
	}
}

func TestTmuxLineNormal(t *testing.T) {
	w := Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := TmuxLine(w, config.Plan{Tier: "max_20x"})
	if !strings.Contains(got, "61%") {
		t.Errorf("missing percent in %q", got)
	}
	if !strings.Contains(got, "1h47m") {
		t.Errorf("missing duration in %q", got)
	}
	if !strings.Contains(got, "#[fg=") {
		t.Errorf("missing fg color escape in %q", got)
	}
}

func TestTmuxLineHot(t *testing.T) {
	w := Window{Percent: 95, MinutesToReset: 10, CeilingLabel: "max_20x"}
	got := TmuxLine(w, config.Plan{Tier: "max_20x"})
	if !strings.Contains(got, "#dc322f") {
		t.Errorf("expected red fg in %q", got)
	}
}

func TestTmuxLineAPITier(t *testing.T) {
	w := Window{Cost5hUSD: 4.21, MinutesToReset: 107, CeilingLabel: "api"}
	got := TmuxLine(w, config.Plan{Tier: "api"})
	if !strings.Contains(got, "$4.21") {
		t.Errorf("expected dollars in %q", got)
	}
}
