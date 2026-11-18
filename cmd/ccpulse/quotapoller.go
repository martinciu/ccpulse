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
	// fetch is the API seam; nil means anthro.Fetch. Tests stub it to drive
	// attempts on a synctest clock without a network.
	fetch func(ctx context.Context, cred anthro.Credential, cacheDir string) (anthro.FetchResult, error)
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
// Every event merges the last successful API result with the overlay. An
// API attempt always sends the result, as the poller did before #568: the
// TUI's recomputeWindow — which re-evaluates the "resets in" countdowns and
// the Fable-row age — has no other trigger in an idle session behind a long
// backoff window. A re-read tick sends only when the merged quota changed.
// When the merge empties out after a send (every bucket expired), one
// QuotaMsg{Usage: nil} follows so the TUI drops the stale bars.
// Returns when ctx is cancelled.
func runQuotaPoller(ctx context.Context, d quotaPollerDeps) {
	fetch := d.fetchFunc()
	var lastAPI *anthro.FetchResult

	pub := &quotaPublisher{cacheDir: d.cacheDir, send: d.send}
	publish := func(force bool) { pub.publish(lastAPI, force) }

	// attempt performs one Fetch, keeps a successful result for the merge,
	// and returns the delay before the next attempt. The WARN lines are per
	// API attempt only; the re-read tick logs nothing (#529's lesson: no log
	// volume per tick).
	attempt := func() time.Duration {
		res, err := fetch(ctx, *d.cred, d.cacheDir)
		delay, consecutive429 := nextPollDelay(res, err, time.Now())
		logPollAttempt(res, err, delay, consecutive429)
		if err != nil {
			return delay
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
		publish(false) // show what the statusline already left behind
	}
	reread := time.NewTicker(overlayRereadInterval)
	defer reread.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-apiC:
			apiTimer.Reset(attempt())
			publish(true)
		case <-reread.C:
			publish(false)
		}
	}
}

// quotaPublisher merges and sends quota messages, remembering the last send
// so unchanged re-read ticks stay quiet.
type quotaPublisher struct {
	cacheDir string
	send     func(tui.QuotaMsg)
	lastSent *anthro.Quota
}

func (p *quotaPublisher) publish(lastAPI *anthro.FetchResult, force bool) {
	now := time.Now()
	q, ok := anthro.MergeQuota(lastAPI, anthro.ReadOverlay(p.cacheDir, now), now)
	if !ok {
		// Every bucket expired and no API result backs the merge. If the
		// TUI is still showing a quota, tell it once to fall back to the
		// JSONL heuristic (as `status` does); lastSent == nil afterwards
		// keeps forced API attempts and re-read ticks quiet until data
		// returns.
		if p.lastSent != nil {
			p.lastSent = nil
			p.send(tui.QuotaMsg{})
		}
		return
	}
	if !force && p.lastSent != nil && sameQuota(*p.lastSent, q) {
		return
	}
	p.lastSent = &q
	u := q.Usage
	p.send(tui.QuotaMsg{
		Usage:        &u,
		Source:       q.Source,
		UpdatedAt:    q.UpdatedAt,
		APIUpdatedAt: q.APIUpdatedAt(),
	})
}

func (d quotaPollerDeps) fetchFunc() func(context.Context, anthro.Credential, string) (anthro.FetchResult, error) {
	if d.fetch != nil {
		return d.fetch
	}
	return anthro.Fetch
}

// logPollAttempt emits the per-API-attempt WARN lines: a failed fetch, or a
// stale cache served in its place. One message, varied by outcome, because
// the branches share an attribute shape. Healthy attempts log nothing here.
func logPollAttempt(res anthro.FetchResult, err error, delay time.Duration, consecutive429 int) {
	switch {
	case err != nil:
		slog.Warn("ccpulse.quotaPoller",
			"outcome", "fetch_error",
			"err", err,
			"next_retry_s", int(delay.Seconds()),
			"consecutive_429", consecutive429)
	case res.Source == "cache_stale":
		slog.Warn("ccpulse.quotaPoller",
			"outcome", "cache_stale",
			"cache_age_s", int(time.Since(res.UpdatedAt).Seconds()),
			"next_retry_s", int(delay.Seconds()),
			"consecutive_429", consecutive429)
	}
}

// sameQuota reports whether two merged quotas would render identically.
func sameQuota(a, b anthro.Quota) bool {
	return a.Source == b.Source &&
		a.UpdatedAt.Equal(b.UpdatedAt) &&
		a.APIUpdatedAt().Equal(b.APIUpdatedAt()) &&
		reflect.DeepEqual(a.Usage, b.Usage)
}
