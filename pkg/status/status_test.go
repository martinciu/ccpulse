package status

import (
	"encoding/json"
	"strings"
	"testing"
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
