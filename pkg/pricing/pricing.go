package pricing

import (
	_ "embed"
	"encoding/json"

	"github.com/martinciu/ccpulse/pkg/parse"
)

//go:embed pricing.json
var embedded []byte

type ModelRate struct {
	InputPerMtok        float64 `json:"input_per_mtok"`
	OutputPerMtok       float64 `json:"output_per_mtok"`
	CacheReadPerMtok    float64 `json:"cache_read_per_mtok"`
	CacheWrite5mPerMtok float64 `json:"cache_write_5m_per_mtok"`
	CacheWrite1hPerMtok float64 `json:"cache_write_1h_per_mtok"`
}

type Table struct {
	Version string               `json:"version"`
	Models  map[string]ModelRate `json:"models"`
}

func Load() (Table, error) {
	var t Table
	err := json.Unmarshal(embedded, &t)
	return t, err
}

// CostFor returns USD estimate for the message and whether the model
// was missing from the pricing table.
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
