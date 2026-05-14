package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
)

//go:embed history/*.json
var historyFS embed.FS

type ModelRate struct {
	InputPerMtok        float64 `json:"input_per_mtok"`
	OutputPerMtok       float64 `json:"output_per_mtok"`
	CacheReadPerMtok    float64 `json:"cache_read_per_mtok"`
	CacheWrite5mPerMtok float64 `json:"cache_write_5m_per_mtok"`
	CacheWrite1hPerMtok float64 `json:"cache_write_1h_per_mtok"`
}

type Table struct {
	Version  string               `json:"version"`
	Currency string               `json:"currency"`
	Models   map[string]ModelRate `json:"models"`
}

// History is the time-indexed set of pricing tables, sorted ascending by Version.
type History struct {
	entries []Table
}

// Load reads every embedded history/*.json file, validates each, and returns
// a History sorted by Version ascending. Duplicate versions are a build error.
func Load() (History, error) {
	files, err := fs.ReadDir(historyFS, "history")
	if err != nil {
		return History{}, fmt.Errorf("pricing: read history dir: %w", err)
	}
	var entries []Table
	seen := make(map[string]string)
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		b, err := fs.ReadFile(historyFS, "history/"+f.Name())
		if err != nil {
			return History{}, fmt.Errorf("pricing: read %s: %w", f.Name(), err)
		}
		t, err := parseTable(b)
		if err != nil {
			return History{}, fmt.Errorf("pricing: %s: %w", f.Name(), err)
		}
		if prev, dup := seen[t.Version]; dup {
			return History{}, fmt.Errorf("pricing: duplicate version %q in %s and %s", t.Version, prev, f.Name())
		}
		seen[t.Version] = f.Name()
		entries = append(entries, t)
	}
	if len(entries) == 0 {
		return History{}, fmt.Errorf("pricing: no history entries embedded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Version < entries[j].Version })
	return History{entries: entries}, nil
}

// parseTable unmarshals raw pricing JSON and validates: currency must be USD,
// version must be non-empty.
func parseTable(b []byte) (Table, error) {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("unmarshal: %w", err)
	}
	if t.Currency != "USD" {
		return t, fmt.Errorf("unsupported currency %q (expected USD)", t.Currency)
	}
	if t.Version == "" {
		return t, fmt.Errorf("missing version field")
	}
	return t, nil
}

// Latest returns the highest-version entry.
func (h History) Latest() Table { return h.entries[len(h.entries)-1] }

// Versions returns sorted version strings of every embedded entry.
func (h History) Versions() []string {
	out := make([]string, len(h.entries))
	for i, e := range h.entries {
		out[i] = e.Version
	}
	return out
}

// TableAt resolves the historical Table that applies to ts. Picks the largest
// entry with Version <= ts.UTC() date string. Falls back to earliest entry if
// ts predates all entries.
func (h History) TableAt(ts time.Time) Table {
	key := ts.UTC().Format("2006-01-02")
	idx := sort.Search(len(h.entries), func(i int) bool {
		return h.entries[i].Version > key
	})
	if idx == 0 {
		return h.entries[0]
	}
	return h.entries[idx-1]
}

// CostFor resolves the historical Table for m.Timestamp and returns cost,
// the resolved version string, and whether the model was missing.
func (h History) CostFor(m parse.Message) (cost float64, version string, unknown bool) {
	tab := h.TableAt(m.Timestamp)
	c, u := tab.CostFor(m)
	return c, tab.Version, u
}

// HistoryForTest builds a History from a caller-provided slice. Intended
// only for tests in other packages; production code uses Load().
func HistoryForTest(entries []Table) (History, error) {
	if len(entries) == 0 {
		return History{}, fmt.Errorf("pricing: HistoryForTest: empty entries")
	}
	cp := make([]Table, len(entries))
	copy(cp, entries)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Version < cp[j].Version })
	return History{entries: cp}, nil
}

// CostFor returns USD estimate for the message and whether the model rate
// was missing from this Table.
func (t Table) CostFor(m parse.Message) (float64, bool) {
	r, ok := t.Models[m.Model]
	if !ok {
		return 0, true
	}
	const M = 1_000_000
	cost := float64(m.InputTokens)/M*r.InputPerMtok +
		float64(m.OutputTokens)/M*r.OutputPerMtok +
		float64(m.CacheReadTokens)/M*r.CacheReadPerMtok +
		float64(m.CacheWrite5mTokens)/M*r.CacheWrite5mPerMtok +
		float64(m.CacheWrite1hTokens)/M*r.CacheWrite1hPerMtok
	return cost, false
}
