package anthro

import "time"

// Bucket is one quota dimension (e.g. five_hour, seven_day_sonnet).
// utilization is a 0-100 percent reported by Anthropic; resets_at is the
// server-side reset boundary in RFC3339Nano.
type Bucket struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// ExtraUsage describes the pay-as-you-go credit pool. Fields can be zero/null
// when the feature is disabled; Utilization is *float64 because the API
// returns null when MonthlyLimit is 0.
type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit float64  `json:"monthly_limit"`
	UsedCredits  float64  `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     string   `json:"currency"`
}

// Usage mirrors the /api/oauth/usage response 1:1. Null buckets are
// represented as nil pointers so they round-trip faithfully.
type Usage struct {
	FiveHour            *Bucket     `json:"five_hour"`
	SevenDay            *Bucket     `json:"seven_day"`
	SevenDayOauthApps   *Bucket     `json:"seven_day_oauth_apps"`
	SevenDayOpus        *Bucket     `json:"seven_day_opus"`
	SevenDaySonnet      *Bucket     `json:"seven_day_sonnet"`
	SevenDayCowork      *Bucket     `json:"seven_day_cowork"`
	SevenDayOmelette    *Bucket     `json:"seven_day_omelette"`
	Tangelo             *Bucket     `json:"tangelo"`
	IguanaNecktie       *Bucket     `json:"iguana_necktie"`
	OmelettePromotional *Bucket     `json:"omelette_promotional"`
	ExtraUsage          *ExtraUsage `json:"extra_usage"`
}
