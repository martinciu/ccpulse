package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// fakeBucket is the element type for the pure-logic resolve tests. It carries
// only a start time so the splice positions can be asserted without a DB.
type fakeBucket struct{ start time.Time }

// fakeQuery returns one fakeBucket per `dur` interval in [from, to), recording
// the ranges it was called with. Mirrors IOTokenBuckets' contiguous-bucket
// contract (len = (to-from)/dur, oldest-first) so resolve's positional splice
// can be checked against a known-good full query.
type fakeQuery struct {
	calls [][2]time.Time // {from, to} per call
}

func (q *fakeQuery) run(_ context.Context, dur time.Duration, from, to time.Time) ([]fakeBucket, error) {
	q.calls = append(q.calls, [2]time.Time{from, to})
	if !to.After(from) {
		return nil, nil
	}
	n := int(to.Sub(from) / dur)
	out := make([]fakeBucket, n)
	for i := range n {
		out[i] = fakeBucket{start: from.Add(time.Duration(i) * dur)}
	}
	return out, nil
}

func starts(bs []fakeBucket) []time.Time {
	out := make([]time.Time, len(bs))
	for i, b := range bs {
		out[i] = b.start
	}
	return out
}

func TestSlotResolve_MissThenStitch(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	earliest := from
	var s slot[fakeBucket]
	q := &fakeQuery{}

	// First call: cold slot → MISS → full query [from, to0).
	to0 := from.Add(4 * dur)
	got0, err := s.resolve(t.Context(), dur, from, to0, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (miss): %v", err)
	}
	if len(q.calls) != 1 || !q.calls[0][0].Equal(from) || !q.calls[0][1].Equal(to0) {
		t.Fatalf("miss query range = %v, want [%v,%v)", q.calls, from, to0)
	}
	if len(got0) != 4 {
		t.Fatalf("miss len = %d, want 4", len(got0))
	}

	// Second call: same key, no boundary crossed (to unchanged) → STITCH
	// re-queries exactly the trailing bucket [to0-dur, to0).
	got1, err := s.resolve(t.Context(), dur, from, to0, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (stitch same to): %v", err)
	}
	last := q.calls[len(q.calls)-1]
	if !last[0].Equal(to0.Add(-dur)) || !last[1].Equal(to0) {
		t.Fatalf("stitch query range = [%v,%v), want [%v,%v)", last[0], last[1], to0.Add(-dur), to0)
	}
	// Byte-identical to a fresh full query over the same window.
	want := mustFull(t, dur, from, to0)
	if !equalStarts(starts(got1), starts(want)) {
		t.Fatalf("stitched starts = %v, want %v", starts(got1), starts(want))
	}

	// Third call: two boundaries crossed → STITCH re-queries [to0-dur, to2).
	to2 := to0.Add(2 * dur)
	got2, err := s.resolve(t.Context(), dur, from, to2, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (stitch crossed): %v", err)
	}
	last = q.calls[len(q.calls)-1]
	if !last[0].Equal(to0.Add(-dur)) || !last[1].Equal(to2) {
		t.Fatalf("crossed stitch range = [%v,%v), want [%v,%v)", last[0], last[1], to0.Add(-dur), to2)
	}
	want = mustFull(t, dur, from, to2)
	if !equalStarts(starts(got2), starts(want)) {
		t.Fatalf("crossed stitched starts = %v, want %v", starts(got2), starts(want))
	}
}

func TestSlotResolve_KeyChangeForcesMiss(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var s slot[fakeBucket]
	q := &fakeQuery{}

	if _, err := s.resolve(t.Context(), dur, from, to, from, q.run); err != nil {
		t.Fatal(err)
	}
	// earliest changes (backfill widened history) → full re-query.
	newEarliest := from.Add(-dur)
	if _, err := s.resolve(t.Context(), dur, from, to, newEarliest, q.run); err != nil {
		t.Fatal(err)
	}
	last := q.calls[len(q.calls)-1]
	if !last[0].Equal(from) || !last[1].Equal(to) {
		t.Fatalf("earliest-change query = [%v,%v), want full [%v,%v)", last[0], last[1], from, to)
	}
}

func TestSlotResolve_ErrorDoesNotMutate(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var s slot[fakeBucket]
	good := &fakeQuery{}
	if _, err := s.resolve(t.Context(), dur, from, to, from, good.run); err != nil {
		t.Fatal(err)
	}
	prevLen := len(s.buckets)

	boom := func(context.Context, time.Duration, time.Time, time.Time) ([]fakeBucket, error) {
		return nil, errors.New("db down")
	}
	if _, err := s.resolve(t.Context(), dur, from, to.Add(dur), from, boom); err == nil {
		t.Fatal("resolve should propagate query error")
	}
	if len(s.buckets) != prevLen || !s.to.Equal(to) {
		t.Fatalf("slot mutated after error: len %d→%d, to=%v", prevLen, len(s.buckets), s.to)
	}
}

func TestChartCacheInvalidate(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var cc chartCache
	tokenQuery := func(_ context.Context, d time.Duration, f, tt time.Time) ([]cache.TokenBucket, error) {
		return tokenBucketsLike(d, f, tt), nil
	}
	// Warm the tokens slot, invalidate, then assert the next resolve is a MISS
	// (it re-queries instead of stitching). A second resolve with the SAME key
	// after the warm would otherwise stitch (1 trailing-bucket query); the
	// invalidate must force a full query regardless.
	if _, err := cc.tokens.resolve(t.Context(), dur, from, to, from, tokenQuery); err != nil {
		t.Fatal(err)
	}
	cc.invalidate()
	if cc.tokens.key.valid {
		t.Fatal("invalidate did not clear tokens.key.valid")
	}

	calls := 0
	counting := func(ctx context.Context, d time.Duration, f, tt time.Time) ([]cache.TokenBucket, error) {
		calls++
		// A MISS queries the full [from, to); a STITCH would query
		// [to-dur, to). Assert the full range to prove it was a MISS.
		if !f.Equal(from) || !tt.Equal(to) {
			t.Errorf("post-invalidate query range = [%v,%v), want full [%v,%v) (MISS)", f, tt, from, to)
		}
		return tokenBucketsLike(d, f, tt), nil
	}
	if _, err := cc.tokens.resolve(t.Context(), dur, from, to, from, counting); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("after invalidate, query calls = %d, want 1", calls)
	}
}

// tokenBucketsLike builds a contiguous []cache.TokenBucket for the
// invalidate test (real element type, zero tokens).
func tokenBucketsLike(dur time.Duration, from, to time.Time) []cache.TokenBucket {
	if !to.After(from) {
		return nil
	}
	n := int(to.Sub(from) / dur)
	out := make([]cache.TokenBucket, n)
	for i := range n {
		out[i] = cache.TokenBucket{BucketStart: from.Add(time.Duration(i) * dur)}
	}
	return out
}

func mustFull(t *testing.T, dur time.Duration, from, to time.Time) []fakeBucket {
	t.Helper()
	fresh := &fakeQuery{}
	out, err := fresh.run(t.Context(), dur, from, to)
	if err != nil {
		t.Fatalf("mustFull: %v", err)
	}
	return out
}

func equalStarts(a, b []time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}
