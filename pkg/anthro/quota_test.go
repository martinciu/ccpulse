package anthro

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMergeQuota(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	R5 := now.Add(2 * time.Hour)
	R7 := now.Add(72 * time.Hour)

	// apiUsage is a full API answer: both quota buckets plus the API-only
	// fields that must always come through from the API side.
	apiUsage := func(five, seven *Bucket) Usage {
		fable := "Fable"
		util := 12.5
		return Usage{
			FiveHour:       five,
			SevenDay:       seven,
			SevenDaySonnet: bk(5, R7),
			ExtraUsage:     &ExtraUsage{IsEnabled: true, MonthlyLimit: 2000, Utilization: &util, Currency: "EUR"},
			Limits: []Limit{{
				Kind: "weekly_scoped", Group: "weekly", Percent: 35,
				Scope: &LimitScope{Model: &ScopeModel{DisplayName: &fable}},
			}},
		}
	}
	ob := func(u float64, at, asOf, acc time.Time) *OverlayBucket {
		at = at.UTC()
		return &OverlayBucket{Bucket: Bucket{Utilization: u, ResetsAt: &at}, AsOf: asOf, AcceptedAt: acc}
	}
	util := func(b *Bucket) any {
		if b == nil {
			return nil
		}
		return b.Utilization
	}

	tests := []struct {
		name          string
		api           *FetchResult
		ov            Overlay
		wantOK        bool
		want5, want7  any // utilization, or nil for a nil bucket
		wantSource    string
		wantUpdatedAt time.Time
		wantAPIAt     time.Time // zero → APIUpdatedAt() must be zero
		check         func(t *testing.T, q Quota)
	}{
		{
			name:          "API only",
			api:           &FetchResult{Usage: apiUsage(bk(5, R5), bk(89, R7)), Source: "cache_stale", UpdatedAt: ago(2 * time.Hour)},
			wantOK:        true,
			want5:         5.0,
			want7:         89.0,
			wantSource:    "cache_stale",
			wantUpdatedAt: ago(2 * time.Hour),
			wantAPIAt:     ago(2 * time.Hour),
			check: func(t *testing.T, q Quota) {
				t.Helper()
				if want := apiUsage(bk(5, R5), bk(89, R7)); !reflect.DeepEqual(q.Usage, want) {
					t.Errorf("Usage = %+v, want the API usage unchanged", q.Usage)
				}
			},
		},
		{
			name: "overlay only",
			ov: Overlay{
				FiveHour: ob(3, R5, ago(2*time.Minute), ago(5*time.Minute)),
				SevenDay: ob(54, R7, ago(time.Minute), ago(time.Hour)),
			},
			wantOK:        true,
			want5:         3.0,
			want7:         54.0,
			wantSource:    SourceStatusline,
			wantUpdatedAt: ago(2 * time.Minute), // oldest as_of shown
			check: func(t *testing.T, q Quota) {
				t.Helper()
				if q.Usage.Limits != nil || q.Usage.ExtraUsage != nil {
					t.Errorf("overlay-only Usage carries API-only fields: %+v", q.Usage)
				}
				if q.API != nil {
					t.Errorf("API = %+v, want nil", q.API)
				}
			},
		},
		{
			name: "overlay newer for both",
			api:  &FetchResult{Usage: apiUsage(bk(5, R5), bk(89, R7)), Source: "cache_stale", UpdatedAt: ago(2 * time.Hour)},
			ov: Overlay{
				FiveHour: ob(7, R5, ago(time.Minute), ago(time.Minute)),
				SevenDay: ob(91, R7, ago(time.Minute), ago(time.Minute)),
			},
			wantOK:        true,
			want5:         7.0,
			want7:         91.0,
			wantSource:    SourceStatusline,
			wantUpdatedAt: ago(time.Minute),
			wantAPIAt:     ago(2 * time.Hour),
			check: func(t *testing.T, q Quota) {
				t.Helper()
				api := apiUsage(nil, nil)
				if !reflect.DeepEqual(q.Usage.Limits, api.Limits) ||
					!reflect.DeepEqual(q.Usage.ExtraUsage, api.ExtraUsage) ||
					!reflect.DeepEqual(q.Usage.SevenDaySonnet, api.SevenDaySonnet) {
					t.Errorf("API-only fields not carried from the API: %+v", q.Usage)
				}
			},
		},
		{
			name:          "API newer than overlay",
			api:           &FetchResult{Usage: apiUsage(bk(5, R5), bk(89, R7)), Source: "api", UpdatedAt: ago(time.Minute)},
			ov:            Overlay{SevenDay: ob(80, R7, ago(30*time.Second), ago(time.Hour))},
			wantOK:        true,
			want5:         5.0,
			want7:         89.0,
			wantSource:    "api",
			wantUpdatedAt: ago(time.Minute),
			wantAPIAt:     ago(time.Minute),
		},
		{
			name:          "mixed: overlay 7d, API 5h",
			api:           &FetchResult{Usage: apiUsage(bk(5, R5), bk(89, R7)), Source: "cache_stale", UpdatedAt: ago(2 * time.Hour)},
			ov:            Overlay{SevenDay: ob(91, R7, ago(time.Minute), ago(time.Minute))},
			wantOK:        true,
			want5:         5.0,
			want7:         91.0,
			wantSource:    SourceStatusline,
			wantUpdatedAt: ago(2 * time.Hour), // oldest shown: the API's 5h
			wantAPIAt:     ago(2 * time.Hour),
		},
		{
			// A nil bucket shows no number (status falls back to the JSONL
			// heuristic for 5h), so it carries no age into UpdatedAt.
			name:          "mixed: overlay 7d, API 5h nil",
			api:           &FetchResult{Usage: apiUsage(nil, bk(89, R7)), Source: "cache_stale", UpdatedAt: ago(2 * time.Hour)},
			ov:            Overlay{SevenDay: ob(91, R7, ago(time.Minute), ago(time.Minute))},
			wantOK:        true,
			want5:         nil,
			want7:         91.0,
			wantSource:    SourceStatusline,
			wantUpdatedAt: ago(time.Minute),
			wantAPIAt:     ago(2 * time.Hour),
		},
		{
			name:          "API newer with FiveHour nil beats older overlay 5h",
			api:           &FetchResult{Usage: apiUsage(nil, bk(89, R7)), Source: "api", UpdatedAt: ago(time.Minute)},
			ov:            Overlay{FiveHour: ob(30, R5, ago(10*time.Second), ago(time.Hour))},
			wantOK:        true,
			want5:         nil,
			want7:         89.0,
			wantSource:    "api",
			wantUpdatedAt: ago(time.Minute),
			wantAPIAt:     ago(time.Minute),
		},
		{
			// The confirm-bump trace: as_of is fresh but the numbers were
			// accepted 3h ago, before the API saw the reset.
			name:          "stale re-report vs newer API",
			api:           &FetchResult{Usage: apiUsage(nil, bk(0, R7)), Source: "cache_fresh", UpdatedAt: ago(time.Hour)},
			ov:            Overlay{SevenDay: ob(95, R7, ago(time.Minute), ago(3*time.Hour))},
			wantOK:        true,
			want5:         nil,
			want7:         0.0,
			wantSource:    "cache_fresh",
			wantUpdatedAt: ago(time.Hour),
			wantAPIAt:     ago(time.Hour),
		},
		{name: "neither", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var before []byte
			if tt.api != nil {
				var err error
				if before, err = json.Marshal(tt.api.Usage); err != nil {
					t.Fatal(err)
				}
			}
			q, ok := MergeQuota(tt.api, tt.ov, now)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got := util(q.Usage.FiveHour); got != tt.want5 {
				t.Errorf("FiveHour = %v, want %v", got, tt.want5)
			}
			if got := util(q.Usage.SevenDay); got != tt.want7 {
				t.Errorf("SevenDay = %v, want %v", got, tt.want7)
			}
			if q.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", q.Source, tt.wantSource)
			}
			if !q.UpdatedAt.Equal(tt.wantUpdatedAt) {
				t.Errorf("UpdatedAt = %v, want %v", q.UpdatedAt, tt.wantUpdatedAt)
			}
			if got := q.APIUpdatedAt(); !got.Equal(tt.wantAPIAt) {
				t.Errorf("APIUpdatedAt() = %v, want %v", got, tt.wantAPIAt)
			}
			if tt.check != nil {
				tt.check(t, q)
			}
			if tt.api != nil {
				// The merge must never write through the API's pointers.
				if q.Usage.FiveHour != nil {
					q.Usage.FiveHour.Utilization = -1
				}
				if q.Usage.SevenDay != nil {
					q.Usage.SevenDay.Utilization = -1
				}
				after, err := json.Marshal(tt.api.Usage)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Errorf("MergeQuota mutated api.Usage:\nbefore %s\nafter  %s", before, after)
				}
			}
		})
	}
}

func TestAPICacheTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	at := func(asOf time.Time) Overlay { return Overlay{SevenDay: &OverlayBucket{AsOf: asOf}} }
	tests := []struct {
		name string
		ov   Overlay
		want time.Duration
	}{
		{"fresh overlay → slow cadence", at(now.Add(-9 * time.Minute)), slowCacheTTL},
		{"one ns under the freshness window", at(now.Add(-StatuslineFreshFor + time.Nanosecond)), slowCacheTTL},
		{"exactly the freshness window", at(now.Add(-StatuslineFreshFor)), cacheTTL},
		{"stale overlay → base TTL", at(now.Add(-11 * time.Minute)), cacheTTL},
		{"empty overlay → base TTL", Overlay{}, cacheTTL},
		// as_of is an untrusted stamp read off disk, like usage.json's
		// updated_at: a future one must not hold the slow cadence open.
		{"future as_of → base TTL", at(now.Add(5 * time.Minute)), cacheTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := apiCacheTTL(tt.ov, now); got != tt.want {
				t.Errorf("apiCacheTTL = %v, want %v", got, tt.want)
			}
		})
	}
}
