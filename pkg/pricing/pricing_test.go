package pricing

import (
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/parse"
)

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

func TestCostFor(t *testing.T) {
	tab, _ := Load()
	m := parse.Message{
		Model:              "claude-opus-4-7",
		InputTokens:        1_000_000, // 1 Mtok
		OutputTokens:       0,
		CacheReadTokens:    0,
		CacheWrite5mTokens: 0,
		CacheWrite1hTokens: 0,
	}
	cost, unknown := tab.CostFor(m)
	if unknown {
		t.Fatal("opus-4-7 should be known")
	}
	want := tab.Models["claude-opus-4-7"].InputPerMtok
	if cost != want {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

func TestCostForUnknown(t *testing.T) {
	tab, _ := Load()
	m := parse.Message{Model: "claude-future-9-9", InputTokens: 100}
	cost, unknown := tab.CostFor(m)
	if !unknown {
		t.Error("expected unknown=true")
	}
	if cost != 0 {
		t.Errorf("cost = %v, want 0", cost)
	}
}

func TestParseTable(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string // substring to look for in err.Error(); empty means expect nil error
		wantVer string // expected Table.Version when no error
	}{
		{
			name:    "usd_accepted",
			data:    `{"version":"test","currency":"USD","models":{}}`,
			wantVer: "test",
		},
		{
			name:    "non_usd_rejected",
			data:    `{"version":"test","currency":"EUR","models":{}}`,
			wantErr: `pricing.json: unsupported currency "EUR" (expected USD)`,
		},
		{
			name:    "missing_currency_rejected",
			data:    `{"version":"test","models":{}}`,
			wantErr: `pricing.json: unsupported currency "" (expected USD)`,
		},
		{
			name:    "missing_version_rejected",
			data:    `{"currency":"USD","models":{}}`,
			wantErr: "pricing.json: missing version field",
		},
		{
			name:    "malformed_json_rejected",
			data:    `{not json`,
			wantErr: "pricing.json:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab, err := parseTable([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("parseTable(%q) returned error: %v", tt.data, err)
				}
				if tab.Version != tt.wantVer {
					t.Errorf("Version = %q, want %q", tab.Version, tt.wantVer)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseTable(%q) returned nil error, want error containing %q", tt.data, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
