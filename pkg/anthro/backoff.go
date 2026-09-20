package anthro

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

// Retry-policy knobs (#447, #529).
//
// BaseRetryInterval is the healthy cadence: how long a caller that polls
// Fetch in a loop should wait between attempts, and the deadline Fetch
// records after a failure that is not a 429. It matches cacheTTL, so a
// poller on this cadence finds the cache exactly at its TTL boundary.
//
// Consecutive 429s escalate the deadline exponentially up to backoffCap;
// the server's Retry-After may push past that cap but never past
// retryAfterMax, so a garbage or hostile header cannot wedge fetching for
// longer than an hour.
const (
	BaseRetryInterval = 3 * time.Minute
	backoffCap        = 30 * time.Minute
	retryAfterMax     = time.Hour
)

// readCeilingSlack widens the ceiling readBackoffState accepts, and only
// there. A maximal window is written at exactly now+retryAfterMax (the
// endpoint asked for an hour or more), so the smallest backward step of the
// wall clock between that write and the next read — an NTP correction is
// enough — would put a perfectly legitimate deadline past a tight ceiling,
// discard it, and send another request to an endpoint that just asked for
// an hour of quiet. The slack does not reopen the wedge this ceiling exists
// to prevent: read never rewrites the value it accepted, so an accepted
// window still drains within retryAfterMax+readCeilingSlack.
const readCeilingSlack = time.Minute

// maxConsecutive429 bounds the escalation counter read back from disk. The
// escalation itself saturates at backoffCap after four steps, so the clamp
// buys nothing for the delay — it exists so a corrupt file holding
// math.MaxInt64 cannot overflow the counter into a negative number (which
// would silently reset the escalation) and cannot print an absurd figure in
// `doctor` or the poller's WARN line.
const maxConsecutive429 = 1000

// The persisted backoff window lives in a sibling of usage.json rather than
// inside it. usage.json is the data cache: touching it on a failure path
// would either bump UpdatedAt (making stale data look fresh) or — when there
// is no cache at all — create an entry holding a zero-value Usage that the
// cache_stale path would happily serve as 0% utilisation. A sibling keeps
// the data cache untouched and resets by deletion.
const (
	backoffFileName = "usage-backoff.json"
	backoffVersion  = 1
)

// ErrBackoff reports that Fetch declined to call the usage API because a
// persisted backoff window is still open. It only ever surfaces wrapped in
// a *RetryError, and only when there is no cached usage to fall back on.
var ErrBackoff = errors.New("usage api backoff in effect")

// RetryError is a fetch that produced no usable usage data, carrying the
// deadline before which callers must not try again. Err is always non-nil:
// the underlying API failure, or ErrBackoff when the attempt was suppressed
// by an already-open window.
//
// The message is deliberately just the wrapped cause, so the string a caller
// prints is byte-identical to the pre-#529 "anthro fetch: api status 429".
// The deadline travels structurally, recovered with errors.As.
type RetryError struct {
	RetryAt        time.Time
	Consecutive429 int
	Err            error
}

func (e *RetryError) Error() string { return "anthro fetch: " + e.Err.Error() }

func (e *RetryError) Unwrap() error { return e.Err }

// BackoffState is the persisted usage-API backoff window. A zero RetryAt
// means no window has been recorded; a RetryAt in the past means the last
// one has drained. Consecutive429 survives a drained window on purpose —
// clearing it there would flatten the escalation to a permanent 6 minutes,
// since every window would start over from zero.
type BackoffState struct {
	RetryAt        time.Time
	Consecutive429 int
}

// Active reports whether the window is still closed at now.
func (s BackoffState) Active(now time.Time) bool { return s.RetryAt.After(now) }

// backoffFile is the on-disk shape. Versioned like the usage cache: an
// unrecognised v is treated as corrupt, which fails open.
type backoffFile struct {
	V              int       `json:"v"`
	RetryAt        time.Time `json:"retry_at"`
	Consecutive429 int       `json:"consecutive_429"`
}

// nextBackoff computes the window that follows a failed attempt, given the
// consecutive-429 count carried over from the previous one and the API
// status this attempt observed (nil when the failure carried no HTTP status
// — transport or decode).
//
// Any outcome other than 429 resets the escalation and returns the base
// cadence: an offline machine must stop firing a timing-out request every
// five seconds just as surely as a rate-limited one must stop hammering.
// The one failure this is never asked about is a cancelled context — Fetch
// filters that out before it gets here, because a caller giving up is not
// evidence about the endpoint.
// A 429 returns max(exp, min(RetryAfter, retryAfterMax)) where
// exp = min(BaseRetryInterval·2ⁿ, backoffCap) and n counts consecutive
// 429s — 6 → 12 → 24 → 30 → 30… minutes when no Retry-After is present.
func nextBackoff(prev int, apiStatus *StatusError) (consecutive int, delay time.Duration) {
	if apiStatus == nil || apiStatus.Code != http.StatusTooManyRequests {
		return 0, BaseRetryInterval
	}
	n := prev + 1
	exp := BaseRetryInterval
	for i := 0; i < n && exp < backoffCap; i++ {
		exp *= 2
	}
	exp = min(exp, backoffCap)
	return n, max(exp, min(apiStatus.RetryAfter, retryAfterMax))
}

func backoffPath(cacheDir string) string { return filepath.Join(cacheDir, backoffFileName) }

// ReadBackoffState reports the persisted backoff window for cacheDir. A
// non-nil error means the state file exists but could not be used; Fetch
// ignores it and proceeds (fail open), while `doctor` surfaces it. The
// returned state is the zero value in that case.
func ReadBackoffState(cacheDir string, now time.Time) (BackoffState, error) {
	return readBackoffState(backoffPath(cacheDir), now)
}

// readBackoffState loads the window at path. Everything here is untrusted
// input from disk, so every failure mode resolves to "no window": a missing,
// truncated, hand-edited or wrong-version file must never be able to stop
// ccpulse from fetching.
//
// The same reasoning covers a retry_at further out than retryAfterMax. This
// code can never write one, so such a value is corrupt by construction —
// clock skew, a botched edit, a half-written byte. Capping it to
// now+retryAfterMax on every read would look like a fix and behave like a
// wedge: each read would push the ceiling forward again and fetching would
// never resume. Discarding it is the only treatment that actually drains.
func readBackoffState(path string, now time.Time) (BackoffState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BackoffState{}, nil
		}
		return BackoffState{}, err
	}
	var f backoffFile
	if err := json.Unmarshal(b, &f); err != nil {
		return BackoffState{}, fmt.Errorf("parse backoff state: %w", err)
	}
	if f.V != backoffVersion {
		return BackoffState{}, fmt.Errorf("backoff state version %d, want %d", f.V, backoffVersion)
	}
	if f.RetryAt.After(now.Add(retryAfterMax + readCeilingSlack)) {
		return BackoffState{}, fmt.Errorf("backoff retry_at is beyond the %s ceiling", retryAfterMax)
	}
	return BackoffState{
		RetryAt:        f.RetryAt,
		Consecutive429: min(max(f.Consecutive429, 0), maxConsecutive429),
	}, nil
}

// writeBackoffState persists the window atomically, with the same 0700/0600
// discipline writeCache applies to usage.json — a sibling process must never
// observe a half-written deadline.
func writeBackoffState(path string, st BackoffState) error {
	out, err := json.MarshalIndent(backoffFile{
		V:              backoffVersion,
		RetryAt:        st.RetryAt.UTC(),
		Consecutive429: st.Consecutive429,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := secfile.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return secfile.WriteFileAtomic(path, out)
}
