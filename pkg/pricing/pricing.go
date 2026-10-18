// Package pricing embeds pricing.json and computes the USD cost of a message.
package pricing

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math"
	"sort"
	"strconv"
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
	// LongContext is a value, not a pointer, so ModelRate stays comparable
	// with == (snapshot carry-forward tests rely on it).
	LongContext LongContextRate `json:"long_context,omitzero"`
}

// LongContextRate is an optional second price tier for models the pricing
// page prices by prompt length. The zero value means "no tier".
type LongContextRate struct {
	OverPromptTokens    int64   `json:"over_prompt_tokens"`
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
// version must be non-empty, the models object must be present and non-empty,
// and every model's rates must be present and positive finite numbers. A
// missing or misspelled "models" key would otherwise load as an empty table
// that prices every message unknown. A missing or misspelled rate key in the source
// JSON is indistinguishable from an explicit 0 once encoding/json has
// unmarshalled it into a float64 zero value, so parseTable rejects it rather
// than silently pricing that dimension free. A genuinely free-tier rate has
// no precedent in the embedded snapshots and would need an explicit design
// decision (e.g. a dedicated sentinel), not a parse-time default. A model may
// also carry an optional "long_context" tier (threshold plus five rates); a
// present tier is held to the same rule, and its threshold must be positive,
// so a misspelled "over_prompt_tokens" key is rejected rather than pricing
// every message long.
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
	if len(t.Models) == 0 {
		return t, errors.New("no models (missing or empty \"models\" object)")
	}
	if err := validateModels(t.Models); err != nil {
		return t, err
	}
	return t, nil
}

// validateModels checks that every ModelRate in models has all five rate
// fields set to a positive, finite number. A model with a non-zero
// LongContext tier must also have a positive threshold and five positive,
// finite tier rates; the zero value means untiered and is valid. Model names
// are visited in sorted order, and per model the base fields, then the
// threshold, then the tier rates in struct declaration order, so the first
// validation failure is deterministic across runs.
func validateModels(models map[string]ModelRate) error {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			return errors.New("model name must not be empty")
		}
		r := models[name]
		if err := validateRates(name, "",
			r.InputPerMtok, r.OutputPerMtok, r.CacheReadPerMtok, r.CacheWrite5mPerMtok, r.CacheWrite1hPerMtok,
		); err != nil {
			return err
		}
		lc := r.LongContext
		if lc == (LongContextRate{}) {
			continue
		}
		if lc.OverPromptTokens <= 0 {
			return fmt.Errorf("model %q: long_context.over_prompt_tokens must be positive, got %d", name, lc.OverPromptTokens)
		}
		if err := validateRates(name, "long_context.",
			lc.InputPerMtok, lc.OutputPerMtok, lc.CacheReadPerMtok, lc.CacheWrite5mPerMtok, lc.CacheWrite1hPerMtok,
		); err != nil {
			return err
		}
	}
	return nil
}

// validateRates checks the five per-Mtok rates, given in ModelRate
// declaration order, and names a failing field by its JSON key behind prefix.
func validateRates(name, prefix string, input, output, cacheRead, cacheWrite5m, cacheWrite1h float64) error {
	fields := [...]struct {
		json string
		rate float64
	}{
		{"input_per_mtok", input},
		{"output_per_mtok", output},
		{"cache_read_per_mtok", cacheRead},
		{"cache_write_5m_per_mtok", cacheWrite5m},
		{"cache_write_1h_per_mtok", cacheWrite1h},
	}
	for _, f := range fields {
		// IsNaN and IsInf are load-bearing: NaN <= 0 and +Inf <= 0 are
		// both false, so the range check alone would let them through.
		if math.IsNaN(f.rate) || math.IsInf(f.rate, 0) || f.rate <= 0 {
			return fmt.Errorf("model %q: %s%s must be a positive finite rate, got %v", name, prefix, f.json, f.rate)
		}
	}
	return nil
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

// ContentHash returns a deterministic FNV-64a hash, as lowercase hex, over the
// full content of every embedded snapshot in h — not just the version
// strings Versions() exposes. Any edit to any snapshot (a new model, a
// corrected rate, a long_context tier, an added file) changes the returned
// value, even when the set of version strings stays the same. Consumed by
// pkg/cache to decide whether a startup recost is needed (issue #512).
func (h History) ContentHash() string {
	sum := fnv.New64a()
	// Strings are written with %q so each one is self-delimiting: a model
	// name containing '|' or '\n' cannot produce the same byte stream as a
	// differently-structured table.
	for _, tab := range h.entries { // h.entries is already sorted by Version
		fmt.Fprintf(sum, "%q|%q\n", tab.Version, tab.Currency)
		names := make([]string, 0, len(tab.Models))
		for name := range tab.Models {
			names = append(names, name)
		}
		sort.Strings(names) // map iteration order must not affect the hash
		for _, name := range names {
			r := tab.Models[name]
			fmt.Fprintf(sum, "%q|%s|%s|%s|%s|%s",
				name,
				strconv.FormatFloat(r.InputPerMtok, 'g', -1, 64),
				strconv.FormatFloat(r.OutputPerMtok, 'g', -1, 64),
				strconv.FormatFloat(r.CacheReadPerMtok, 'g', -1, 64),
				strconv.FormatFloat(r.CacheWrite5mPerMtok, 'g', -1, 64),
				strconv.FormatFloat(r.CacheWrite1hPerMtok, 'g', -1, 64),
			)
			// The tier goes behind a "|lc|" marker only when present, so
			// untiered lines stay byte-identical to the pre-tier encoding
			// and a tiered line's extra fields cannot collide with one.
			if lc := r.LongContext; lc != (LongContextRate{}) {
				fmt.Fprintf(sum, "|lc|%s|%s|%s|%s|%s|%s",
					strconv.FormatInt(lc.OverPromptTokens, 10),
					strconv.FormatFloat(lc.InputPerMtok, 'g', -1, 64),
					strconv.FormatFloat(lc.OutputPerMtok, 'g', -1, 64),
					strconv.FormatFloat(lc.CacheReadPerMtok, 'g', -1, 64),
					strconv.FormatFloat(lc.CacheWrite5mPerMtok, 'g', -1, 64),
					strconv.FormatFloat(lc.CacheWrite1hPerMtok, 'g', -1, 64),
				)
			}
			fmt.Fprintln(sum)
		}
	}
	return strconv.FormatUint(sum.Sum64(), 16)
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
//
// A model with a LongContext tier bills every token kind, output included,
// at the tier rates when the message's prompt is strictly greater than
// OverPromptTokens; exactly at the threshold stays on the base rates. The
// prompt counts input, cache read and both cache writes; output tokens never
// count toward it. Accepted approximation: a parent row whose tokens pkg/parse
// summed from several own-model usage.iterations entries is tiered on the
// sum, although the pricing page prices each request on its own.
func (t Table) CostFor(m parse.Message) (float64, bool) {
	r, ok := t.Models[m.Model]
	if !ok {
		return 0, true
	}
	prompt := m.InputTokens + m.CacheReadTokens + m.CacheWrite5mTokens + m.CacheWrite1hTokens
	if lc := r.LongContext; lc != (LongContextRate{}) && prompt > lc.OverPromptTokens {
		r = ModelRate{
			InputPerMtok:        lc.InputPerMtok,
			OutputPerMtok:       lc.OutputPerMtok,
			CacheReadPerMtok:    lc.CacheReadPerMtok,
			CacheWrite5mPerMtok: lc.CacheWrite5mPerMtok,
			CacheWrite1hPerMtok: lc.CacheWrite1hPerMtok,
		}
	}
	const M = 1_000_000
	cost := float64(m.InputTokens)/M*r.InputPerMtok +
		float64(m.OutputTokens)/M*r.OutputPerMtok +
		float64(m.CacheReadTokens)/M*r.CacheReadPerMtok +
		float64(m.CacheWrite5mTokens)/M*r.CacheWrite5mPerMtok +
		float64(m.CacheWrite1hTokens)/M*r.CacheWrite1hPerMtok
	return cost, false
}
