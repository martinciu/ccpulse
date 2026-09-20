package main

import (
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
