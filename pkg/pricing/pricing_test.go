package pricing

import (
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/parse"
)

func TestLoadEmbedded(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tab := h.Latest()
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
	h, _ := Load()
	tab := h.Latest()
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
	h, _ := Load()
	tab := h.Latest()
	m := parse.Message{Model: "claude-future-9-9", InputTokens: 100}
	cost, unknown := tab.CostFor(m)
	if !unknown {
		t.Error("expected unknown=true")
	}
	if cost != 0 {
		t.Errorf("cost = %v, want 0", cost)
	}
}

func TestHistory_Load_AllEmbedded(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	versions := h.Versions()
	if len(versions) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d: %v", len(versions), versions)
	}
	for i := 1; i < len(versions); i++ {
		if versions[i-1] >= versions[i] {
			t.Errorf("Versions() not strictly ascending: %v", versions)
		}
	}
}

func TestHistory_Latest(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	latest := h.Latest()
	versions := h.Versions()
	if latest.Version != versions[len(versions)-1] {
		t.Errorf("Latest().Version = %q, want last of Versions() = %q", latest.Version, versions[len(versions)-1])
	}
	if latest.Currency != "USD" {
		t.Errorf("Latest().Currency = %q, want USD", latest.Currency)
	}
}

func TestParseTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		data             string
		wantErr          string  // substring to look for in err.Error(); empty means expect nil error
		wantVer          string  // expected Table.Version when no error
		wantModelKey     string  // if non-empty, assert tab.Models has this key
		wantInputPerMtok float64 // expected InputPerMtok for wantModelKey
	}{
		{
			name:    "usd_accepted",
			data:    `{"version":"test","currency":"USD","models":{}}`,
			wantVer: "test",
		},
		{
			name:             "happy_path_with_models",
			data:             `{"version":"test","currency":"USD","models":{"claude-opus-4-7":{"input_per_mtok":5}}}`,
			wantVer:          "test",
			wantModelKey:     "claude-opus-4-7",
			wantInputPerMtok: 5,
		},
		{
			name:    "non_usd_rejected",
			data:    `{"version":"test","currency":"EUR","models":{}}`,
			wantErr: `unsupported currency "EUR" (expected USD)`,
		},
		{
			name:    "missing_currency_rejected",
			data:    `{"version":"test","models":{}}`,
			wantErr: `unsupported currency "" (expected USD)`,
		},
		{
			name:    "missing_version_rejected",
			data:    `{"currency":"USD","models":{}}`,
			wantErr: "missing version field",
		},
		{
			name:    "malformed_json_rejected",
			data:    `{not json`,
			wantErr: "unmarshal:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tab, err := parseTable([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("parseTable(%q) returned error: %v", tt.data, err)
				}
				if tab.Version != tt.wantVer {
					t.Errorf("Version = %q, want %q", tab.Version, tt.wantVer)
				}
				if tt.wantModelKey != "" {
					rate, ok := tab.Models[tt.wantModelKey]
					if !ok {
						t.Errorf("Models[%q] missing", tt.wantModelKey)
					} else if rate.InputPerMtok != tt.wantInputPerMtok {
						t.Errorf("Models[%q].InputPerMtok = %v, want %v", tt.wantModelKey, rate.InputPerMtok, tt.wantInputPerMtok)
					}
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
