package pricing

import "testing"

func TestLoadEmbedded(t *testing.T) {
	tab, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if tab.Version == "" {
		t.Error("Version empty")
	}
	op, ok := tab.Models["claude-opus-4-7"]
	if !ok {
		t.Fatal("opus-4-7 missing")
	}
	if op.InputPerMtok <= 0 {
		t.Errorf("input_per_mtok = %v", op.InputPerMtok)
	}
}
