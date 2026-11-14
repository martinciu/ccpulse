package anthro

import "time"

// SourceStatusline is the Quota.Source (and `status --json` quota_source)
// value when at least one displayed 5h/7d bucket came from the statusline
// overlay (#568).
const SourceStatusline = "statusline"

// StatuslineFreshFor is how long statusline data counts as fresh: while the
// overlay's newest as_of is younger than this, the API cache TTL stretches
// to slowCacheTTL, and the TUI shows no stale badge for statusline quota.
const StatuslineFreshFor = 10 * time.Minute

// slowCacheTTL is the usage-API cache TTL while statusline data is fresh:
// the API then only serves what the statusline doesn't carry (the Fable row,
// extra usage, the less-used buckets), so ≈2 req/h instead of ≈20. A var so
// tests can override it, like cacheTTL.
var slowCacheTTL = 30 * time.Minute

// Quota is the merged quota every reader displays: the newest 5h/7d reading
// from either source, everything else from the API.
type Quota struct {
	Usage Usage
	// Source is SourceStatusline when at least one displayed 5h/7d bucket
	// came from the overlay, else the API result's Source.
	Source string
	// UpdatedAt is the oldest freshness stamp among the non-nil 5h/7d
	// buckets shown (overlay as_of, or the API's UpdatedAt), so "fresh as
	// of" is never overstated. A nil bucket shows no number and carries no
	// age.
	UpdatedAt time.Time
	// API is the raw API result the merge started from (nil when there was
	// none). It carries the retry deadline and the API side's own age.
	API *FetchResult
}

// APIUpdatedAt is when the API-sourced fields (Limits / Fable row,
// ExtraUsage, the other buckets) were fetched; zero when there is no API
// data.
func (q Quota) APIUpdatedAt() time.Time {
	if q.API == nil {
		return time.Time{}
	}
	return q.API.UpdatedAt
}

// MergeQuota merges the API result with the statusline overlay (spec §4).
// Per 5h/7d bucket, the overlay wins when present and its AcceptedAt is
// newer than the API data (or there is no API data); otherwise the API's
// bucket wins, even when nil — a newer API "idle" beats older statusline
// numbers. AsOf never decides precedence. Every other field comes from the
// API as-is, so limits[weekly_all] may disagree with an overlaid seven_day.
//
// Overlay buckets whose reset has passed at now are ignored. ok is false
// only when neither source has data. api.Usage is never written through:
// the result's 5h/7d buckets are fresh copies.
func MergeQuota(api *FetchResult, ov Overlay, now time.Time) (Quota, bool) {
	ov = liveOverlay(ov, now)
	if api == nil && ov.Empty() {
		return Quota{}, false
	}
	q := Quota{API: api}
	if api != nil {
		q.Usage = api.Usage // shallow copy; the two buckets are replaced below
		q.Source = api.Source
	}
	m := bucketMerger{api: api}
	q.Usage.FiveHour = m.pick(q.Usage.FiveHour, ov.FiveHour)
	q.Usage.SevenDay = m.pick(q.Usage.SevenDay, ov.SevenDay)
	if m.fromOverlay {
		q.Source = SourceStatusline
	}
	q.UpdatedAt = m.oldest
	if q.UpdatedAt.IsZero() { // no non-nil bucket shown
		if api != nil {
			q.UpdatedAt = api.UpdatedAt
		} else {
			q.UpdatedAt = ov.NewestAsOf()
		}
	}
	return q, true
}

// liveOverlay drops overlay buckets whose reset has passed at now.
func liveOverlay(ov Overlay, now time.Time) Overlay {
	live := func(b *OverlayBucket) *OverlayBucket {
		if b == nil || b.ResetsAt == nil || !b.ResetsAt.After(now) {
			return nil
		}
		return b
	}
	return Overlay{FiveHour: live(ov.FiveHour), SevenDay: live(ov.SevenDay)}
}

// bucketMerger applies MergeQuota's per-bucket precedence and tracks what
// the result needs: whether any shown bucket came from the overlay, and the
// oldest freshness stamp among the non-nil buckets shown.
type bucketMerger struct {
	api         *FetchResult
	fromOverlay bool
	oldest      time.Time
}

func (m *bucketMerger) note(t time.Time) {
	if m.oldest.IsZero() || t.Before(m.oldest) {
		m.oldest = t
	}
}

// pick returns a fresh copy of the winning bucket: the overlay's when it was
// accepted after the API data was fetched, else the API's (possibly nil).
func (m *bucketMerger) pick(apiB *Bucket, ovB *OverlayBucket) *Bucket {
	if ovB != nil && (m.api == nil || ovB.AcceptedAt.After(m.api.UpdatedAt)) {
		m.fromOverlay = true
		m.note(ovB.AsOf)
		return copyBucket(&ovB.Bucket)
	}
	if apiB != nil {
		m.note(m.api.UpdatedAt)
	}
	return copyBucket(apiB)
}

func copyBucket(b *Bucket) *Bucket {
	if b == nil {
		return nil
	}
	c := *b
	if b.ResetsAt != nil {
		t := *b.ResetsAt
		c.ResetsAt = &t
	}
	return &c
}

// apiCacheTTL picks the usage-API cache TTL: slowCacheTTL while the
// overlay's newest as_of is younger than StatuslineFreshFor, else cacheTTL.
// A future as_of is untrusted (clock stepped back, hand edit) and counts as
// stale, the same rule freshFromCache applies to usage.json's updated_at.
func apiCacheTTL(ov Overlay, now time.Time) time.Duration {
	asOf := ov.NewestAsOf()
	if asOf.IsZero() {
		return cacheTTL
	}
	if age := now.Sub(asOf); age >= 0 && age < StatuslineFreshFor {
		return slowCacheTTL
	}
	return cacheTTL
}
