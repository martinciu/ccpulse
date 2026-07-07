package main

import (
	"net/http"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// Quota-poller cadence knobs (#447). basePollInterval is the healthy
// cadence. Consecutive 429s escalate the delay exponentially up to
// backoffCap; the server's Retry-After may push past the cap but never
// past retryAfterMax, so a garbage or hostile header can't wedge the
// poller for longer than an hour.
const (
	basePollInterval = 3 * time.Minute
	backoffCap       = 30 * time.Minute
	retryAfterMax    = time.Hour
)

// pollBackoff computes the delay before the next quota poll. The zero
// value is ready to use. Not safe for concurrent use — the poller
// goroutine owns it.
type pollBackoff struct {
	consecutive429 int
}

// next returns the delay before the next poll given the API status the
// last attempt observed: nil when the attempt saw no non-2xx status
// (success, fresh cache, transport or decode failure).
//
// Any outcome other than 429 resets the escalation and returns the base
// cadence. A 429 returns max(exp, min(RetryAfter, retryAfterMax)) where
// exp = min(basePollInterval·2ⁿ, backoffCap) and n counts consecutive
// 429s — 6 → 12 → 24 → 30 → 30… minutes when no Retry-After is present.
func (b *pollBackoff) next(apiStatus *anthro.StatusError) time.Duration {
	if apiStatus == nil || apiStatus.Code != http.StatusTooManyRequests {
		b.consecutive429 = 0
		return basePollInterval
	}
	b.consecutive429++
	exp := basePollInterval
	for i := 0; i < b.consecutive429 && exp < backoffCap; i++ {
		exp *= 2
	}
	exp = min(exp, backoffCap)
	return max(exp, min(apiStatus.RetryAfter, retryAfterMax))
}
