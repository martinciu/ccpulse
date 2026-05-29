// Package pricing embeds pricing.json and computes the USD cost of a message.
package pricing

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
)

//go:embed history/*.json
var historyFS embed.FS

// ModelRate holds the per-million-token billing rates for one model.
type ModelRate struct {
	InputPerMtok        float64 `json:"input_per_mtok"`
	OutputPerMtok       float64 `json:"output_per_mtok"`
	CacheReadPerMtok    float64 `json:"cache_read_per_mtok"`
	CacheWrite5mPerMtok float64 `json:"cache_write_5m_per_mtok"`
	CacheWrite1hPerMtok float64 `json:"cache_write_1h_per_mtok"`
}

// Table is a versioned snapshot of per-model billing rates decoded from a pricing JSON file.
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
		return History{}, errors.New("pricing: no history entries embedded")
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
		return t, errors.New("missing version field")
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

// resolveTable returns the Table whose rates apply to (ts, model). It starts at
// the date-resolved table (the same entry TableAt picks) and, if that table does
// not contain model, walks FORWARD through later entries (ascending Version),
// returning the earliest one that does. found is false only when no entry
// at-or-after the date-resolved one knows model; in that case the returned Table
// is the date-resolved one, so callers stamp a sensible version.
//
// The walk is forward-only by design: a model present only in an entry EARLIER
// than the date-resolved one would be a retired rate, and applying it backward
// is unsafe (issue #368).
func (h History) resolveTable(ts time.Time, model string) (Table, bool) {
	key := ts.UTC().Format("2006-01-02")
	idx := sort.Search(len(h.entries), func(i int) bool {
		return h.entries[i].Version > key
	})
	if idx > 0 {
		idx-- // date-resolved index (matches TableAt)
	}
	for j := idx; j < len(h.entries); j++ {
		if _, ok := h.entries[j].Models[model]; ok {
			return h.entries[j], true
		}
	}
	return h.entries[idx], false
}

// CostFor resolves the applicable Table for m via fall-forward (see resolveTable)
// and returns cost, the stamped version string, and whether the model was missing
// from every snapshot at-or-after the date-resolved one. The stamped version is
// the snapshot whose rates produced the cost — the fall-forward source when the
// date-resolved snapshot predates the model.
func (h History) CostFor(m parse.Message) (cost float64, version string, unknown bool) {
	tab, found := h.resolveTable(m.Timestamp, m.Model)
	if !found {
		return 0, tab.Version, true
	}
	c, _ := tab.CostFor(m) // found ⇒ inner unknown is always false
	return c, tab.Version, false
}

// VersionFor returns the pricing_version CostFor would stamp for a message with
// this timestamp and model, applying the same fall-forward resolution, without
// needing token counts. Used by staleness checks (PricingVersionStats).
func (h History) VersionFor(ts time.Time, model string) string {
	tab, _ := h.resolveTable(ts, model)
	return tab.Version
}

// HistoryForTest builds a History from a caller-provided slice.
//
// Test-only helper. Exported because Go does not let _test.go in one
// package be imported by tests in another. Do not call from production
// code — use Load() instead, which reads the embedded history/*.json.
func HistoryForTest(entries []Table) (History, error) {
	if len(entries) == 0 {
		return History{}, errors.New("pricing: HistoryForTest: empty entries")
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
