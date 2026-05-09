package pricing

import (
	_ "embed"
	"encoding/json"
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
