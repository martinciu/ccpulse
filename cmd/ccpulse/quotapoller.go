package main

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/tui"
)

// overlayRereadInterval is how often the TUI re-reads the statusline
// overlay (#568), independent of the API cadence — which can sit behind a
// backoff window of up to an hour. A re-read touches files only: no Fetch,
// no API attempt, no log line. A var so tests can shorten it.
var overlayRereadInterval = 30 * time.Second

// quotaPollerDeps wires runQuotaPoller. cred == nil runs overlay-only: no
// API attempts at all (no OAuth credential). recordAPI, when set, receives
// every raw FetchResult whose Source is "api" — never the merged quota, so
// an overlaid bucket is never filed as an API sample.
type quotaPollerDeps struct {
	cred      *anthro.Credential
	cacheDir  string
	send      func(tui.QuotaMsg)
	recordAPI func(ctx context.Context, res anthro.FetchResult)
}

// runQuotaPoller feeds the TUI quota from two loops sharing one goroutine
// and one select:
//
//   - API attempts (only with a credential): the first fires immediately,
//     then on the per-outcome cadence nextPollDelay derives —
//     anthro.BaseRetryInterval normally, the persisted backoff deadline
//     otherwise (#447, #529). The policy lives in pkg/anthro, so the TUI and
//     the statusline back off together; between the API's slow-cadence
//     fetches an attempt just returns cache_fresh from disk.
//   - Overlay re-reads every overlayRereadInterval, so statusline readings —
//     and buckets whose window expired — reach the header within one tick.
//
// Every event merges the last successful API result with the overlay and
// sends a QuotaMsg only when the merged quota changed. Returns when ctx is
// cancelled.
func runQuotaPoller(ctx context.Context, d quotaPollerDeps) {
	var lastAPI *anthro.FetchResult
	var lastSent *anthro.Quota

	publish := func() {
		now := time.Now()
		q, ok := anthro.MergeQuota(lastAPI, anthro.ReadOverlay(d.cacheDir, now), now)
		if !ok || (lastSent != nil && sameQuota(*lastSent, q)) {
			return
		}
		lastSent = &q
		u := q.Usage
		d.send(tui.QuotaMsg{
			Usage:        &u,
			Source:       q.Source,
			UpdatedAt:    q.UpdatedAt,
			APIUpdatedAt: q.APIUpdatedAt(),
		})
	}

	// attempt performs one Fetch, keeps a successful result for the merge,
	// and returns the delay before the next attempt. The WARN lines are per
	// API attempt only; the re-read tick logs nothing (#529's lesson: no log
	// volume per tick).
	attempt := func() time.Duration {
		res, err := anthro.Fetch(ctx, *d.cred, d.cacheDir)
		delay, consecutive429 := nextPollDelay(res, err, time.Now())
		if err != nil {
			slog.Warn("ccpulse.quotaPoller",
				"outcome", "fetch_error",
				"err", err,
				"next_retry_s", int(delay.Seconds()),
				"consecutive_429", consecutive429)
			return delay
		}
		if res.Source == "cache_stale" {
			slog.Warn("ccpulse.quotaPoller",
				"outcome", "cache_stale",
				"cache_age_s", int(time.Since(res.UpdatedAt).Seconds()),
				"next_retry_s", int(delay.Seconds()),
				"consecutive_429", consecutive429)
		}
		if res.Source == "api" && d.recordAPI != nil {
			d.recordAPI(ctx, res)
		}
		lastAPI = &res
		return delay
	}

	// A nil channel never fires: without a credential the API arm is inert
	// and the loop is re-reads only.
	var apiC <-chan time.Time
	var apiTimer *time.Timer
	if d.cred != nil {
		apiTimer = time.NewTimer(0) // immediate first attempt
		defer apiTimer.Stop()
		apiC = apiTimer.C
	} else {
		publish() // show what the statusline already left behind
	}
	reread := time.NewTicker(overlayRereadInterval)
	defer reread.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-apiC:
			apiTimer.Reset(attempt())
			publish()
		case <-reread.C:
			publish()
		}
	}
}

// sameQuota reports whether two merged quotas would render identically.
func sameQuota(a, b anthro.Quota) bool {
	return a.Source == b.Source &&
		a.UpdatedAt.Equal(b.UpdatedAt) &&
		a.APIUpdatedAt().Equal(b.APIUpdatedAt()) &&
		reflect.DeepEqual(a.Usage, b.Usage)
}
