package status

import "encoding/json"

type Window struct {
	Percent        int     `json:"percent"`
	MinutesToReset int     `json:"minutes_to_reset"`
	CeilingLabel   string  `json:"ceiling_label"`
	Tokens5h       int64   `json:"tokens_5h"`
	Cost5hUSD      float64 `json:"cost_5h_usd"`
}

func JSON(w Window) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}
