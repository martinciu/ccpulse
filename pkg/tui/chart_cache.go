package tui

import (
	"context"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// chartCache memoizes the per-bar-unit bucket arrays so refreshChart avoids
// re-aggregating the full messages history on every RefreshMsg (#378). One
// slot per bar unit; the line-mode (chartUnitRemaining) path reads
// usage_samples and is deliberately not cached.
//
// Owned solely by the Bubble Tea Update goroutine — refreshChart runs
// synchronously inside handleRefresh, and every consumer copies values out of
// the returned slice immediately (loadTokenSeries / loadCostSeries build fresh
// []float64 / []time.Time before the next refresh). No mutex; no slice escapes
// past one refresh.
type chartCache struct {
	tokens slot[cache.TokenBucket]
	cost   slot[cache.CostBucket]
}

// invalidate marks both slots stale so the next resolve does a full query.
// Forward-looking: the messages table is never pruned today (only
// usage_samples is). Any FUTURE messages prune / delete path MUST call this —
// otherwise resolve would splice a fresh tail onto a now-incorrect cached
// prefix and serve stale totals. Not wired to a call site yet; the method
// exists so that contract is explicit rather than a latent bug.
func (c *chartCache) invalidate() {
	c.tokens.key.valid = false
	c.cost.key.valid = false
}

// slot is one memoized bucket array plus the view-parameter key it was
// computed for. Generic over the bucket element type (TokenBucket / CostBucket)
// — the splice is purely positional, so no element introspection is needed.
type slot[T any] struct {
	key     slotKey
	buckets []T       // oldest-first, contiguous from key.from
	to      time.Time // exclusive upper bound the buckets cover
}

// slotKey identifies the view the cached buckets belong to. A mismatch on any
// field (or valid == false) forces a full re-query.
type slotKey struct {
	zoom     time.Duration
	from     time.Time
	earliest time.Time // mirrors EarliestMessageTime; a change means backfill widened history leftward
	valid    bool
}

// queryFn is the cache aggregator signature shared by IOTokenBuckets and
// CostBuckets (passed as method values), parameterised by element type.
type queryFn[T any] func(context.Context, time.Duration, time.Time, time.Time) ([]T, error)

// stitchBoundary is the lower bound of the trailing region to re-query: the
// start of the bucket that was open at the previous refresh. `to` is always a
// bucket boundary (BucketAlign for sub-day, local midnight for 24h), so for
// sub-day zooms this is to-dur; for 24h it is the previous local midnight
// (AddDate is DST-safe — `to` is already local midnight, so stepping back one
// calendar day lands on the prior midnight regardless of a 23h/25h transition).
func stitchBoundary(to time.Time, dur time.Duration) time.Time {
	if dur == 24*time.Hour {
		return to.AddDate(0, 0, -1)
	}
	return to.Add(-dur)
}

// resolve returns the full [from, to) bucket array for the slot's unit. On a
// key match it reuses the sealed cached prefix and re-queries only
// [stitchBoundary(slot.to), to); otherwise it does a full query. The result is
// byte-identical to query(ctx, dur, from, to). On a query error the slot is
// left unmutated and the error is propagated.
func (s *slot[T]) resolve(
	ctx context.Context,
	dur time.Duration,
	from, to, earliest time.Time,
	query queryFn[T],
) ([]T, error) {
	k := slotKey{zoom: dur, from: from, earliest: earliest, valid: true}

	miss := !s.key.valid ||
		len(s.buckets) == 0 ||
		s.key.zoom != dur ||
		!s.key.from.Equal(from) ||
		!s.key.earliest.Equal(earliest)

	if miss {
		buckets, err := query(ctx, dur, from, to)
		if err != nil {
			return nil, err
		}
		s.buckets, s.to, s.key = buckets, to, k
		return buckets, nil
	}

	// STITCH: re-query the still-mutable trailing region and splice it onto the
	// sealed prefix. The boundary bucket (open last refresh) is always
	// recomputed — it may have grown, and the messages upsert can bump an
	// already-counted turn via max(excluded, existing).
	boundary := stitchBoundary(s.to, dur)
	tail, err := query(ctx, dur, boundary, to)
	if err != nil {
		return nil, err // slot untouched
	}
	// In-place splice: drop the previously-trailing bucket, append the fresh
	// tail. len(buckets) >= 1 (len == 0 is routed to MISS above) and
	// len(tail) >= 1 (to > boundary), so the common no-boundary-crossed case
	// (tail of 1) reuses the backing array with zero reallocation. Safe to
	// mutate in place: nothing holds a reference to the old array past this
	// refresh (single goroutine; callers copy values out immediately).
	n := len(s.buckets)
	merged := append(s.buckets[:n-1], tail...) //nolint:gocritic // intentional in-place splice, see resolve doc
	s.buckets, s.to, s.key = merged, to, k
	return merged, nil
}
