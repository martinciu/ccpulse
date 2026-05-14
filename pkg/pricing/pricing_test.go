package pricing

import (
	"strings"
	"testing"
	"time"

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

func TestHistory_TableAt(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mustTime := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return ts
	}

	cases := []struct {
		name        string
		ts          time.Time
		wantVersion string
	}{
		{"before earliest -> earliest", mustTime("2025-01-01T00:00:00Z"), "2026-05-09"},
		{"exact earliest", mustTime("2026-05-09T00:00:00Z"), "2026-05-09"},
		{"between versions -> preceding", mustTime("2026-05-09T23:59:59Z"), "2026-05-09"},
		{"exact later version", mustTime("2026-05-10T00:00:00Z"), "2026-05-10"},
		{"after latest -> latest", mustTime("2099-01-01T00:00:00Z"), "2026-05-10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.TableAt(c.ts).Version
			if got != c.wantVersion {
				t.Errorf("TableAt(%s).Version = %q, want %q", c.ts.Format(time.RFC3339), got, c.wantVersion)
			}
		})
	}
}

func TestHistory_CostFor_StampsResolvedVersion(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := parse.Message{
		Timestamp:   time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		Model:       "claude-opus-4-7",
		InputTokens: 1_000_000,
	}
	cost, version, unknown := h.CostFor(m)
	if unknown {
		t.Fatalf("expected model known in 2026-05-09 table")
	}
	if version != "2026-05-09" {
		t.Errorf("version = %q, want 2026-05-09 (resolved, not latest)", version)
	}
	if cost <= 0 {
		t.Errorf("cost = %v, want positive", cost)
	}
}

func TestHistory_CostFor_UnknownModel(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := parse.Message{
		Timestamp: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		Model:     "no-such-model-xyz",
	}
	cost, version, unknown := h.CostFor(m)
	if !unknown {
		t.Errorf("expected unknown=true for missing model, got false")
	}
	if cost != 0 {
		t.Errorf("expected cost=0 for unknown model, got %v", cost)
	}
	if version != "2026-05-10" {
		t.Errorf("version = %q, want 2026-05-10 (still resolves)", version)
	}
}
