package pricing

import (
	"fmt"
	"math"
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
			data:    `{"version":"test","currency":"USD","models":{"claude-x":{"input_per_mtok":5,"output_per_mtok":25,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantVer: "test",
		},
		{
			name:    "empty_models_rejected",
			data:    `{"version":"test","currency":"USD","models":{}}`,
			wantErr: "no models",
		},
		{
			// A misspelled "models" key decodes to a nil map — the whole
			// table would silently price every message unknown.
			name:    "missing_models_key_rejected",
			data:    `{"version":"test","currency":"USD","modles":{"claude-x":{"input_per_mtok":5}}}`,
			wantErr: "no models",
		},
		{
			name:             "happy_path_with_models",
			data:             `{"version":"test","currency":"USD","models":{"claude-opus-4-7":{"input_per_mtok":5,"output_per_mtok":25,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
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
		{
			// cache_read_per_mtok key is absent entirely: encoding/json leaves
			// it at the float64 zero value, indistinguishable from an explicit
			// 0 — parseTable must reject it rather than silently pricing the
			// dimension free.
			name:    "missing_rate_key_rejected",
			data:    `{"version":"test","currency":"USD","models":{"claude-x":{"input_per_mtok":5,"output_per_mtok":25,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantErr: `model "claude-x": cache_read_per_mtok must be a positive finite rate, got 0`,
		},
		{
			name:    "zero_rate_rejected",
			data:    `{"version":"test","currency":"USD","models":{"claude-x":{"input_per_mtok":0,"output_per_mtok":25,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantErr: `model "claude-x": input_per_mtok must be a positive finite rate, got 0`,
		},
		{
			name:    "negative_rate_rejected",
			data:    `{"version":"test","currency":"USD","models":{"claude-x":{"input_per_mtok":5,"output_per_mtok":-25,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantErr: `model "claude-x": output_per_mtok must be a positive finite rate, got -25`,
		},
		{
			// Two violating models: the error must name the alphabetically
			// first one regardless of map iteration order. claude-b appears
			// first in the JSON on purpose.
			name:    "first_error_names_alphabetically_first_model",
			data:    `{"version":"test","currency":"USD","models":{"claude-b":{"input_per_mtok":5,"output_per_mtok":0,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10},"claude-a":{"input_per_mtok":5,"output_per_mtok":25,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantErr: `model "claude-a": cache_read_per_mtok must be a positive finite rate, got 0`,
		},
		{
			name:    "empty_model_name_rejected",
			data:    `{"version":"test","currency":"USD","models":{"":{"input_per_mtok":5,"output_per_mtok":25,"cache_read_per_mtok":0.5,"cache_write_5m_per_mtok":6.25,"cache_write_1h_per_mtok":10}}}`,
			wantErr: "model name must not be empty",
		},
		{
			name:             "multi_model_table_accepted",
			data:             `{"version":"test","currency":"USD","models":{"claude-a":{"input_per_mtok":1,"output_per_mtok":2,"cache_read_per_mtok":0.1,"cache_write_5m_per_mtok":1.25,"cache_write_1h_per_mtok":2},"claude-b":{"input_per_mtok":3,"output_per_mtok":4,"cache_read_per_mtok":0.3,"cache_write_5m_per_mtok":3.75,"cache_write_1h_per_mtok":6}}}`,
			wantVer:          "test",
			wantModelKey:     "claude-b",
			wantInputPerMtok: 3,
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

// TestValidateModels_NonFiniteRejected exercises the NaN/Inf branch of
// validateModels directly. encoding/json rejects out-of-range literals
// (e.g. 1e999) before they ever reach a float64, so this path is
// unreachable through parseTable's JSON input and must be tested against
// the helper with a constructed ModelRate instead (issue #514).
func TestValidateModels_NonFiniteRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rate    ModelRate
		wantErr string
	}{
		{
			name:    "nan_input",
			rate:    ModelRate{InputPerMtok: math.NaN(), OutputPerMtok: 1, CacheReadPerMtok: 1, CacheWrite5mPerMtok: 1, CacheWrite1hPerMtok: 1},
			wantErr: `model "claude-x": input_per_mtok must be a positive finite rate, got NaN`,
		},
		{
			name:    "pos_inf_output",
			rate:    ModelRate{InputPerMtok: 1, OutputPerMtok: math.Inf(1), CacheReadPerMtok: 1, CacheWrite5mPerMtok: 1, CacheWrite1hPerMtok: 1},
			wantErr: `model "claude-x": output_per_mtok must be a positive finite rate, got +Inf`,
		},
		{
			name:    "neg_inf_cache_write_1h",
			rate:    ModelRate{InputPerMtok: 1, OutputPerMtok: 1, CacheReadPerMtok: 1, CacheWrite5mPerMtok: 1, CacheWrite1hPerMtok: math.Inf(-1)},
			wantErr: `model "claude-x": cache_write_1h_per_mtok must be a positive finite rate, got -Inf`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateModels(map[string]ModelRate{"claude-x": tt.rate})
			if err == nil {
				t.Fatalf("validateModels(%+v) returned nil error, want error containing %q", tt.rate, tt.wantErr)
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

	versions := h.Versions()
	earliest, latest := versions[0], versions[len(versions)-1]

	cases := []struct {
		name        string
		ts          time.Time
		wantVersion string
	}{
		{"before earliest -> earliest", mustTime("2025-01-01T00:00:00Z"), earliest},
		{"exact earliest", mustTime("2026-05-09T00:00:00Z"), "2026-05-09"},
		{"between versions -> preceding", mustTime("2026-05-09T23:59:59Z"), "2026-05-09"},
		{"exact later version", mustTime("2026-05-10T00:00:00Z"), "2026-05-10"},
		{"day before 2026-07-24 -> 2026-07-01", mustTime("2026-07-23T23:59:59Z"), "2026-07-01"},
		{"intro window end -> 2026-07-24", mustTime("2026-08-31T23:59:59Z"), "2026-07-24"},
		{"standard rates start -> 2026-09-01", mustTime("2026-09-01T00:00:00Z"), "2026-09-01"},
		{"last second before 2026-09-02 -> 2026-09-01", mustTime("2026-09-01T23:59:59Z"), "2026-09-01"},
		{"fable 5.1 snapshot -> 2026-09-02", mustTime("2026-09-02T00:00:00Z"), "2026-09-02"},
		{"after latest -> latest", mustTime("2099-01-01T00:00:00Z"), latest},
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

func TestHistory_CostFor_FallForward(t *testing.T) {
	v1 := Table{Version: "2026-01-01", Currency: "USD", Models: map[string]ModelRate{
		"modelA": {InputPerMtok: 10},
		"modelR": {InputPerMtok: 99}, // retired: present only in the earliest table
	}}
	v2 := Table{Version: "2026-02-01", Currency: "USD", Models: map[string]ModelRate{
		"modelA": {InputPerMtok: 8},
		"modelB": {InputPerMtok: 20},
	}}
	v3 := Table{Version: "2026-03-01", Currency: "USD", Models: map[string]ModelRate{
		"modelA": {InputPerMtok: 6},
		"modelB": {InputPerMtok: 18},
		"modelC": {InputPerMtok: 30},
	}}
	h, err := HistoryForTest([]Table{v1, v2, v3})
	if err != nil {
		t.Fatalf("HistoryForTest: %v", err)
	}
	mustTime := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return ts
	}
	const Mtok = 1_000_000
	tests := []struct {
		name        string
		ts          time.Time
		model       string
		wantVersion string
		wantUnknown bool
		wantCost    float64 // checked only when !wantUnknown
	}{
		{"model in resolved table", mustTime("2026-01-15T00:00:00Z"), "modelA", "2026-01-01", false, 10},
		{"fall-forward one step", mustTime("2026-01-15T00:00:00Z"), "modelB", "2026-02-01", false, 20},
		{"fall-forward earliest of several", mustTime("2026-01-15T00:00:00Z"), "modelC", "2026-03-01", false, 30},
		{"present in resolved, no walk", mustTime("2026-02-15T00:00:00Z"), "modelB", "2026-02-01", false, 20},
		{"unknown everywhere -> date-resolved stamp", mustTime("2026-02-15T00:00:00Z"), "modelZ", "2026-02-01", true, 0},
		{"before earliest -> earliest table", mustTime("2025-12-01T00:00:00Z"), "modelA", "2026-01-01", false, 10},
		{"retired (only earlier) -> not rescued backward", mustTime("2026-02-15T00:00:00Z"), "modelR", "2026-02-01", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parse.Message{Timestamp: tt.ts, Model: tt.model, InputTokens: Mtok}
			cost, version, unknown := h.CostFor(m)
			if unknown != tt.wantUnknown {
				t.Errorf("unknown = %v, want %v", unknown, tt.wantUnknown)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %q, want %q", version, tt.wantVersion)
			}
			if !tt.wantUnknown && cost != tt.wantCost {
				t.Errorf("cost = %v, want %v", cost, tt.wantCost)
			}
			if got := h.VersionFor(tt.ts, tt.model); got != tt.wantVersion {
				t.Errorf("VersionFor = %q, want %q", got, tt.wantVersion)
			}
		})
	}
}

// mustParseDate parses a snapshot version string ("2006-01-02") into the
// midnight-UTC instant TableAt resolves to that exact snapshot.
func mustParseDate(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse version %s: %v", s, err)
	}
	return ts
}

func TestSnapshot20260609_FableAndMythos(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tab := h.TableAt(mustParseDate(t, "2026-06-09"))
	if tab.Version != "2026-06-09" {
		t.Fatalf("TableAt(2026-06-09).Version = %q, want 2026-06-09", tab.Version)
	}
	want := ModelRate{
		InputPerMtok:        10.00,
		OutputPerMtok:       50.00,
		CacheReadPerMtok:    1.00,
		CacheWrite5mPerMtok: 12.50,
		CacheWrite1hPerMtok: 20.00,
	}
	for _, model := range []string{"claude-fable-5", "claude-mythos-5", "fable", "mythos"} {
		got, ok := tab.Models[model]
		if !ok {
			t.Errorf("Models[%q] missing", model)
			continue
		}
		if got != want {
			t.Errorf("Models[%q] = %+v, want %+v", model, got, want)
		}
	}
}

// carryForwardExceptionKey identifies one declared, intentional rate change
// between consecutive snapshots: the NEWER snapshot's version and the model
// whose rate changed.
type carryForwardExceptionKey struct{ version, model string }

// carryForwardExceptions declares every intentional rate change between
// consecutive snapshots for TestHistory_CarryForward_AllSnapshots, keyed by
// the newer snapshot's version and model name. The value is the ModelRate
// expected AFTER the change. Undeclared entries are required to carry
// forward with byte-identical rates; a declared entry must both match this
// value in the newer snapshot and actually differ from the model's rate in
// the immediately preceding snapshot, so a stale declaration (left behind
// after a later edit reverts the change) fails the test. See issue #514.
var carryForwardExceptions = map[carryForwardExceptionKey]ModelRate{
	// Intentional Opus 4.7 price cut that shipped with the 2026-05-10
	// snapshot: input/output/cache rates all dropped roughly 3x from the
	// 2026-05-09 launch pricing.
	{version: "2026-05-10", model: "claude-opus-4-7"}: {
		InputPerMtok:        5.00,
		OutputPerMtok:       25.00,
		CacheReadPerMtok:    0.50,
		CacheWrite5mPerMtok: 6.25,
		CacheWrite1hPerMtok: 10.00,
	},
}

// diffModelRate returns one "field: prev -> cur" string per ModelRate field
// (in JSON-tag name) that differs between prev and cur.
func diffModelRate(prev, cur ModelRate) []string {
	fields := []struct {
		json      string
		prev, cur float64
	}{
		{"input_per_mtok", prev.InputPerMtok, cur.InputPerMtok},
		{"output_per_mtok", prev.OutputPerMtok, cur.OutputPerMtok},
		{"cache_read_per_mtok", prev.CacheReadPerMtok, cur.CacheReadPerMtok},
		{"cache_write_5m_per_mtok", prev.CacheWrite5mPerMtok, cur.CacheWrite5mPerMtok},
		{"cache_write_1h_per_mtok", prev.CacheWrite1hPerMtok, cur.CacheWrite1hPerMtok},
	}
	var diffs []string
	for _, f := range fields {
		if f.prev != f.cur {
			diffs = append(diffs, fmt.Sprintf("%s: %v -> %v", f.json, f.prev, f.cur))
		}
	}
	return diffs
}

// TestHistory_CarryForward_AllSnapshots encodes the snapshot convention:
// every dated file carries every model forward from its predecessor with
// IDENTICAL rates — snapshots only add entries or make an intentional,
// declared rate change (retired models keep their last known rate). An
// intentional change must be declared in carryForwardExceptions; a carried
// rate that drifts without a declaration (e.g. a typo introduced while
// hand-editing a later snapshot) fails the test. See issue #514.
func TestHistory_CarryForward_AllSnapshots(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	versions := h.Versions()
	visited := make(map[carryForwardExceptionKey]bool, len(carryForwardExceptions))
	for i := 1; i < len(versions); i++ {
		prev := h.TableAt(mustParseDate(t, versions[i-1]))
		cur := h.TableAt(mustParseDate(t, versions[i]))
		if prev.Version != versions[i-1] || cur.Version != versions[i] {
			t.Fatalf("TableAt resolved %s/%s, want %s/%s",
				prev.Version, cur.Version, versions[i-1], versions[i])
		}
		for model, prevRate := range prev.Models {
			curRate, ok := cur.Models[model]
			if !ok {
				t.Errorf("model %q present in %s but missing from %s", model, prev.Version, cur.Version)
				continue
			}
			key := carryForwardExceptionKey{version: cur.Version, model: model}
			if want, declared := carryForwardExceptions[key]; declared {
				visited[key] = true
				if curRate != want {
					t.Errorf("declared exception %+v: %s.Models[%q] = %+v, want declared %+v",
						key, cur.Version, model, curRate, want)
				}
				if curRate == prevRate {
					t.Errorf("declared exception %+v is stale: %s.Models[%q] no longer differs from %s.Models[%q] (%+v)",
						key, cur.Version, model, prev.Version, model, prevRate)
				}
				continue
			}
			if curRate != prevRate {
				t.Errorf("model %q rate changed from %s to %s without a declared carryForwardExceptions entry: %s",
					model, prev.Version, cur.Version, strings.Join(diffModelRate(prevRate, curRate), "; "))
			}
		}
	}
	// An exception whose (version, model) never matched a carried-forward
	// pair above (typo'd version/model, or naming a model that isn't
	// actually carried forward from its predecessor) would otherwise sit
	// unverified forever — fail loudly instead.
	for key := range carryForwardExceptions {
		if !visited[key] {
			t.Errorf("carryForwardExceptions entry %+v was never matched against a carried-forward model pair (typo'd version/model?)", key)
		}
	}
}

func TestSonnet5Snapshots(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	intro := ModelRate{
		InputPerMtok:        2.00,
		OutputPerMtok:       10.00,
		CacheReadPerMtok:    0.20,
		CacheWrite5mPerMtok: 2.50,
		CacheWrite1hPerMtok: 4.00,
	}
	for _, tc := range []struct {
		version string
		want    ModelRate
	}{
		{"2026-07-01", intro},
		{"2026-07-24", intro},
		// The $3/$15 increase scheduled for 2026-09-01 was cancelled; the
		// intro rates are the standard price (issue #496).
		{"2026-09-01", intro},
		{"2026-09-02", intro},
	} {
		t.Run(tc.version, func(t *testing.T) {
			tab := h.TableAt(mustParseDate(t, tc.version))
			if tab.Version != tc.version {
				t.Fatalf("TableAt(%s).Version = %q, want %q", tc.version, tab.Version, tc.version)
			}
			got, ok := tab.Models["claude-sonnet-5"]
			if !ok {
				t.Fatalf("Models[claude-sonnet-5] missing from %s", tc.version)
			}
			if got != tc.want {
				t.Errorf("claude-sonnet-5 = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSonnet5Resolution pins timestamp-based resolution across the snapshot
// boundaries: pre-snapshot Sonnet 5 usage falls forward to intro rates (issue
// #368 semantics), and the intro rates persist past 2026-09-01 because the
// scheduled increase was cancelled (issue #496).
func TestSonnet5Resolution(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const Mtok = 1_000_000
	cases := []struct {
		name        string
		ts          time.Time
		wantVersion string
		wantCost    float64
	}{
		{"fall-forward before first snapshot", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), "2026-07-01", 2.00},
		{"intro window last second", time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC), "2026-07-24", 2.00},
		{"intro rates persist from Sept 1", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "2026-09-01", 2.00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parse.Message{Timestamp: tc.ts, Model: "claude-sonnet-5", InputTokens: Mtok}
			cost, version, unknown := h.CostFor(m)
			if unknown {
				t.Fatal("unknown = true, want false")
			}
			if version != tc.wantVersion {
				t.Errorf("version = %q, want %q", version, tc.wantVersion)
			}
			if cost != tc.wantCost {
				t.Errorf("cost = %v, want %v", cost, tc.wantCost)
			}
		})
	}
}

func TestOpus5Snapshots(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := ModelRate{
		InputPerMtok:        5.00,
		OutputPerMtok:       25.00,
		CacheReadPerMtok:    0.50,
		CacheWrite5mPerMtok: 6.25,
		CacheWrite1hPerMtok: 10.00,
	}
	for _, tc := range []struct {
		version string
		want    ModelRate
	}{
		{"2026-07-24", want},
		{"2026-09-01", want},
		{"2026-09-02", want},
	} {
		t.Run(tc.version, func(t *testing.T) {
			tab := h.TableAt(mustParseDate(t, tc.version))
			if tab.Version != tc.version {
				t.Fatalf("TableAt(%s).Version = %q, want %q", tc.version, tab.Version, tc.version)
			}
			got, ok := tab.Models["claude-opus-5"]
			if !ok {
				t.Fatalf("Models[claude-opus-5] missing from %s", tc.version)
			}
			if got != tc.want {
				t.Errorf("claude-opus-5 = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestOpus5Resolution pins the pricing_version stamped and the resolved cost
// for Claude Opus 5 across the 2026-07-24 intro window and the 2026-09-02
// carry-forward table. Resolution walks forward only, so a model absent from
// a later snapshot costs $0 rather than falling back (issue #470).
func TestOpus5Resolution(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const Mtok = 1_000_000
	cases := []struct {
		name        string
		ts          time.Time
		wantVersion string
		wantCost    float64
	}{
		{"fall-forward before snapshot", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), "2026-07-24", 5.00},
		{"exact snapshot date", time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC), "2026-07-24", 5.00},
		{"intro window last second", time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC), "2026-07-24", 5.00},
		{"after fable 5.1 snapshot", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), "2026-09-02", 5.00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parse.Message{Timestamp: tc.ts, Model: "claude-opus-5", InputTokens: Mtok}
			cost, version, unknown := h.CostFor(m)
			if unknown {
				t.Fatal("unknown = true, want false")
			}
			if version != tc.wantVersion {
				t.Errorf("version = %q, want %q", version, tc.wantVersion)
			}
			if cost != tc.wantCost {
				t.Errorf("cost = %v, want %v", cost, tc.wantCost)
			}
		})
	}
}

// TestFable51Snapshots pins the Claude Fable 5.1 / Mythos 5.1 rates introduced
// by the 2026-09-02 snapshot. The cache-read rate is 0.025x base input
// ($0.25/MTok), not the 0.1x every other model uses — the pricing page carries
// an explicit footnote for it (issue #511). The trailing claude-fable-5 check
// proves the new rate was not also applied to the previous generation.
func TestFable51Snapshots(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tab := h.TableAt(mustParseDate(t, "2026-09-02"))
	if tab.Version != "2026-09-02" {
		t.Fatalf("TableAt(2026-09-02).Version = %q, want 2026-09-02", tab.Version)
	}
	want := ModelRate{
		InputPerMtok:        10.00,
		OutputPerMtok:       50.00,
		CacheReadPerMtok:    0.25,
		CacheWrite5mPerMtok: 12.50,
		CacheWrite1hPerMtok: 20.00,
	}
	for _, model := range []string{"claude-fable-5-1", "claude-mythos-5-1"} {
		got, ok := tab.Models[model]
		if !ok {
			t.Errorf("Models[%q] missing from 2026-09-02", model)
			continue
		}
		if got != want {
			t.Errorf("Models[%q] = %+v, want %+v", model, got, want)
		}
	}
	for _, model := range []string{"claude-fable-5", "claude-mythos-5"} {
		prev, ok := tab.Models[model]
		if !ok {
			t.Errorf("Models[%q] missing from 2026-09-02 (carry-forward broken)", model)
			continue
		}
		if prev.CacheReadPerMtok != 1.00 {
			t.Errorf("Models[%q].CacheReadPerMtok = %v, want 1.00 (0.1x rate must stay on the previous generation)", model, prev.CacheReadPerMtok)
		}
	}
}

// TestFable51Resolution pins the pricing_version stamped and the resolved cost
// for Claude Fable 5.1 / Mythos 5.1 around the 2026-09-02 snapshot. Row 1 is
// the rescue path for rows ingested before the snapshot existed (fall-forward,
// issue #368 semantics). The cache-read rows are the reason the snapshot
// exists: $0.25/MTok is 0.025x base input, while the previous generation keeps
// the standard 0.1x ($1.00/MTok) on the same table (issue #511).
func TestFable51Resolution(t *testing.T) {
	h, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const Mtok = 1_000_000
	cases := []struct {
		name        string
		ts          time.Time
		model       string
		input       int64
		cacheRead   int64
		wantVersion string
		wantCost    float64
	}{
		{"fall-forward before snapshot", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), "claude-fable-5-1", Mtok, 0, "2026-09-02", 10.00},
		{"exact snapshot date", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude-fable-5-1", Mtok, 0, "2026-09-02", 10.00},
		{"after snapshot", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), "claude-fable-5-1", Mtok, 0, "2026-09-02", 10.00},
		{"fable 5.1 cache read at 0.025x", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude-fable-5-1", 0, Mtok, "2026-09-02", 0.25},
		{"mythos 5.1 cache read at 0.025x", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude-mythos-5-1", 0, Mtok, "2026-09-02", 0.25},
		{"fable 5 cache read stays 0.1x", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude-fable-5", 0, Mtok, "2026-09-02", 1.00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parse.Message{
				Timestamp:       tc.ts,
				Model:           tc.model,
				InputTokens:     tc.input,
				CacheReadTokens: tc.cacheRead,
			}
			cost, version, unknown := h.CostFor(m)
			if unknown {
				t.Fatal("unknown = true, want false")
			}
			if version != tc.wantVersion {
				t.Errorf("version = %q, want %q", version, tc.wantVersion)
			}
			if cost != tc.wantCost {
				t.Errorf("cost = %v, want %v", cost, tc.wantCost)
			}
		})
	}
}
