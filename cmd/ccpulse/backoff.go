package main

import (
	"errors"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// minPollDelay floors the computed sleep. A deadline can already be in the
// past by the time the poller reads it — a clock jump, or a fetch that took
// longer than the window it was granted — and without a floor the timer
// would fire immediately and turn the loop into a spin.
const minPollDelay = time.Second

// pollDelay converts the retry deadline anthro.Fetch reports into the sleep
// before the next poll. A zero deadline means nothing is backing off, so the
// poller falls back to the healthy cadence.
//
// The escalation policy itself moved into pkg/anthro in #529 and is now
// shared with every short-lived `ccpulse status` process through the
// persisted state file. The TUI used to own a second, in-process copy: it
// politely waited thirty minutes while the statusline next to it fired at
// the same endpoint every five seconds.
func pollDelay(retryAt, now time.Time) time.Duration {
	if retryAt.IsZero() {
		return anthro.BaseRetryInterval
	}
	return max(retryAt.Sub(now), minPollDelay)
}

// nextPollDelay turns one Fetch outcome into the sleep before the next poll
// and the escalation count to log with it. Both Fetch return shapes carry
// the deadline — the result while there is still cached data to serve, a
// *RetryError once there is not — and the poller must honour it either way:
// the no-cache 429 case is precisely where dropping it would spin the timer
// and flood the log, which is the bug #529 set out to kill.
//
// Split out of runQuotaPoller's closure so it can be tested without
// standing up a tea.Program and a cache.
func nextPollDelay(res anthro.FetchResult, err error, now time.Time) (delay time.Duration, consecutive429 int) {
	if err != nil {
		var re *anthro.RetryError
		if errors.As(err, &re) {
			return pollDelay(re.RetryAt, now), re.Consecutive429
		}
		// A failure carrying no deadline at all (an empty access token,
		// say) is not worth a tight retry either.
		return pollDelay(time.Time{}, now), 0
	}
	return pollDelay(res.RetryAt, now), res.Consecutive429
}
