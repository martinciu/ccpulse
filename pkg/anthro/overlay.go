package anthro

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

// The statusline overlay (#568) holds the 5h/7d readings Claude Code hands
// every statusline command, in a sibling of usage.json rather than inside it:
// rewriting usage.json would bump its updated_at, making the API cache look
// fresh (so the API-only Fable row would never refresh) and mixing two
// sources' freshness into one timestamp. Same sibling-file discipline as
// usage-backoff.json: versioned, fail open, atomic writes, its own lock.
const (
	overlayFileName = "usage-statusline.json"
	overlayLockName = "usage-statusline.json.lock"
	overlayVersion  = 1
)

// Freshness knobs for the per-session merge (spec §2).
const (
	// unknownSessionFreshFor: a session ccpulse has never heard from only
	// gets rate_limits after its first API response, so its first report is
	// new information when its transcript was written this recently.
	unknownSessionFreshFor = 5 * time.Minute
	// sessionMemoryTTL / maxSessionMemory bound the per-session memory.
	sessionMemoryTTL = 8 * 24 * time.Hour
	maxSessionMemory = 256
	// sessionSeenRefresh: an idle session re-rendering the same numbers
	// refreshes its seen_at at most this often, so idle re-renders don't
	// rewrite the file.
	sessionSeenRefresh = time.Hour
)

// OverlayBucket is one stored statusline bucket. AsOf is the freshness
// evidence (when these numbers were last confirmed current); AcceptedAt is
// when this value last arrived as new information. Only AcceptedAt decides
// precedence against the API (MergeQuota), so a confirm-bump of stale
// numbers can never lift them over a newer API fetch.
type OverlayBucket struct {
	Bucket
	AsOf       time.Time
	AcceptedAt time.Time
}

// Overlay is the statusline-sourced 5h/7d pair. A nil bucket is absent.
type Overlay struct {
	FiveHour *OverlayBucket
	SevenDay *OverlayBucket
}

// Empty reports whether the overlay holds no bucket.
func (o Overlay) Empty() bool { return o.FiveHour == nil && o.SevenDay == nil }

// NewestAsOf is the latest AsOf among the overlay's buckets; zero when empty.
func (o Overlay) NewestAsOf() time.Time {
	var t time.Time
	for _, b := range []*OverlayBucket{o.FiveHour, o.SevenDay} {
		if b != nil && b.AsOf.After(t) {
			t = b.AsOf
		}
	}
	return t
}

// reportedBucket is a bucket's numbers without freshness metadata: what a
// session reported, or what was last recorded to history.
type reportedBucket struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// same compares two reports: utilisation exactly, resets_at at minute
// precision — the API's 08:59:59.86Z and a header's 09:00:00Z are one window
// (same normalisation pkg/cache applies to 7d bucket identity).
func (a reportedBucket) same(b reportedBucket) bool {
	return a.Utilization == b.Utilization &&
		a.ResetsAt.Round(time.Minute).Equal(b.ResetsAt.Round(time.Minute))
}

// sameReport compares two optional reports; both absent counts as the same.
func sameReport(a, b *reportedBucket) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.same(*b)
}

type storedBucket struct {
	reportedBucket
	AsOf       time.Time `json:"as_of"`
	AcceptedAt time.Time `json:"accepted_at"`
}

type sessionReport struct {
	FiveHour *reportedBucket `json:"five_hour,omitempty"`
	SevenDay *reportedBucket `json:"seven_day,omitempty"`
	SeenAt   time.Time       `json:"seen_at"`
}

// recordedSample is the last statusline reading written to usage_samples
// (spec §6), so a settled reading is recorded once.
type recordedSample struct {
	At       time.Time       `json:"at"`
	FiveHour *reportedBucket `json:"five_hour,omitempty"`
	SevenDay *reportedBucket `json:"seven_day,omitempty"`
}

// overlayFile is the on-disk envelope. Session ids live only here (a 0600
// file) and are never logged.
type overlayFile struct {
	V        int                      `json:"v"`
	FiveHour *storedBucket            `json:"five_hour,omitempty"`
	SevenDay *storedBucket            `json:"seven_day,omitempty"`
	Sessions map[string]sessionReport `json:"sessions,omitempty"`
	Recorded *recordedSample          `json:"recorded,omitempty"`
}

func overlayPath(cacheDir string) string { return filepath.Join(cacheDir, overlayFileName) }

// toReported drops a parsed bucket's pointer shape. A bucket without a reset
// can't be placed in a window and counts as absent (ParseStatusline never
// produces one).
func toReported(b *Bucket) *reportedBucket {
	if b == nil || b.ResetsAt == nil {
		return nil
	}
	return &reportedBucket{Utilization: b.Utilization, ResetsAt: b.ResetsAt.UTC()}
}

// mergeReading folds one statusline reading into the stored overlay (spec
// §2–§3) and reports whether anything changed. It is pure: f is not mutated.
//
// Whether the reading is new information is decided once per reading:
//   - no session id: per bucket, it may only start the stored value, raise it
//     within the same window, or start a newer window — never lower it;
//   - a session ccpulse hasn't seen: new when its transcript activity is
//     within unknownSessionFreshFor;
//   - a known session: new when a bucket present in both its last report and
//     this one changed, or a bucket newly appeared. A bucket dropping out is
//     not new — Claude Code omits a bucket once its reset passes, with no API
//     call.
//
// New information replaces a stored bucket (or confirms an equal one); a
// re-report equal to the stored bucket confirms it up to the transcript
// activity; anything else is ignored, so an idle session can never overwrite
// fresher numbers.
func mergeReading(f overlayFile, r StatuslineReading, activity, now time.Time) (overlayFile, bool) {
	now = now.UTC()
	out := f
	out.V = overlayVersion
	out.Sessions = maps.Clone(f.Sessions)
	in5, in7 := toReported(r.FiveHour), toReported(r.SevenDay)

	var c5, c7 bool
	if r.SessionID == "" {
		out.FiveHour, c5 = mergeBucket(f.FiveHour, in5, raisesOrNewWindow(f.FiveHour, in5), activity, now)
		out.SevenDay, c7 = mergeBucket(f.SevenDay, in7, raisesOrNewWindow(f.SevenDay, in7), activity, now)
	} else {
		isNew := sessionReadingIsNew(f.Sessions, r.SessionID, in5, in7, activity, now)
		out.FiveHour, c5 = mergeBucket(f.FiveHour, in5, isNew, activity, now)
		out.SevenDay, c7 = mergeBucket(f.SevenDay, in7, isNew, activity, now)
	}
	var cs bool
	out.Sessions, cs = rememberSession(out.Sessions, r.SessionID, in5, in7, now)
	pruned := pruneSessions(out.Sessions, now)
	return out, c5 || c7 || cs || pruned
}

// sessionReadingIsNew decides, once per reading, whether a session's report
// is new information (see mergeReading).
func sessionReadingIsNew(sessions map[string]sessionReport, id string, in5, in7 *reportedBucket, activity, now time.Time) bool {
	known, isKnown := sessions[id]
	if !isKnown {
		return !activity.IsZero() && now.Sub(activity) <= unknownSessionFreshFor
	}
	return newInformation(known, in5, in7)
}

// rememberSession records what session id just reported. It rewrites the
// entry only when the values changed or its seen_at is older than
// sessionSeenRefresh, so idle re-renders leave the file alone. A reading
// without a session id is not remembered. sessions is mergeReading's clone,
// so writing to it is safe.
func rememberSession(sessions map[string]sessionReport, id string, in5, in7 *reportedBucket, now time.Time) (map[string]sessionReport, bool) {
	if id == "" {
		return sessions, false
	}
	known, isKnown := sessions[id]
	valuesChanged := !sameReport(known.FiveHour, in5) || !sameReport(known.SevenDay, in7)
	if isKnown && !valuesChanged && now.Sub(known.SeenAt) <= sessionSeenRefresh {
		return sessions, false
	}
	if sessions == nil {
		sessions = make(map[string]sessionReport)
	}
	sessions[id] = sessionReport{FiveHour: in5, SevenDay: in7, SeenAt: now}
	return sessions, true
}

// newInformation reports whether a known session's reading carries numbers
// it has not reported before: a bucket present in both reports changed, or a
// bucket newly appeared.
func newInformation(known sessionReport, in5, in7 *reportedBucket) bool {
	changedOrAppeared := func(last, in *reportedBucket) bool {
		return in != nil && (last == nil || !last.same(*in))
	}
	return changedOrAppeared(known.FiveHour, in5) || changedOrAppeared(known.SevenDay, in7)
}

// raisesOrNewWindow is the acceptance rule for a reading with no session id:
// it may start the stored value, raise it within the same window, or start a
// newer window — never lower it.
func raisesOrNewWindow(stored *storedBucket, in *reportedBucket) bool {
	if in == nil {
		return false
	}
	if stored == nil {
		return true
	}
	if !in.ResetsAt.Round(time.Minute).Equal(stored.ResetsAt.Round(time.Minute)) {
		return in.ResetsAt.After(stored.ResetsAt)
	}
	return in.Utilization > stored.Utilization
}

// mergeBucket applies one bucket of a reading to the stored bucket.
func mergeBucket(stored *storedBucket, in *reportedBucket, isNew bool, activity, now time.Time) (*storedBucket, bool) {
	if in == nil {
		return stored, false // an absent bucket leaves the stored one untouched
	}
	equal := stored != nil && stored.same(*in)
	switch {
	case isNew && !equal:
		return &storedBucket{reportedBucket: *in, AsOf: now, AcceptedAt: now}, true
	case isNew:
		if stored.AsOf.Equal(now) {
			return stored, false
		}
		c := *stored
		c.AsOf = now
		return &c, true
	case equal && activity.After(stored.AsOf):
		c := *stored
		c.AsOf = activity.UTC()
		return &c, true
	default:
		return stored, false
	}
}

// pruneSessions drops session memory older than sessionMemoryTTL, then the
// oldest entries beyond maxSessionMemory. It mutates s (a clone owned by
// mergeReading) and reports whether it removed anything.
func pruneSessions(s map[string]sessionReport, now time.Time) bool {
	pruned := false
	for id, rep := range s {
		if now.Sub(rep.SeenAt) > sessionMemoryTTL {
			delete(s, id)
			pruned = true
		}
	}
	if len(s) <= maxSessionMemory {
		return pruned
	}
	ids := slices.Collect(maps.Keys(s))
	slices.SortFunc(ids, func(a, b string) int {
		if c := s[a].SeenAt.Compare(s[b].SeenAt); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	for _, id := range ids[:len(ids)-maxSessionMemory] {
		delete(s, id)
	}
	return true
}

// readOverlayRaw loads the envelope at path. A missing file is the zero
// envelope with a nil error; anything else unusable is an error.
func readOverlayRaw(path string) (overlayFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return overlayFile{}, nil
		}
		return overlayFile{}, err
	}
	var f overlayFile
	if err := json.Unmarshal(b, &f); err != nil {
		return overlayFile{}, fmt.Errorf("parse statusline overlay: %w", err)
	}
	if f.V != overlayVersion {
		return overlayFile{}, fmt.Errorf("statusline overlay version %d, want %d", f.V, overlayVersion)
	}
	return f, nil
}

func writeOverlayRaw(path string, f overlayFile) error {
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := secfile.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return secfile.WriteFileAtomic(path, out)
}

// IngestStatusline merges one reading into <cacheDir>/usage-statusline.json
// under the overlay's own lock — never usage.json.lock, which Fetch holds
// across an HTTP call — and writes only when something changed. A corrupt or
// unknown-version file is treated as absent and rewritten. If the lock can't
// be taken, it proceeds unlocked, like Fetch.
func IngestStatusline(cacheDir string, r StatuslineReading, now time.Time) (changed bool, err error) {
	if release, lockErr := acquireFileLock(cacheDir, overlayLockName); lockErr == nil {
		defer release()
	}
	path := overlayPath(cacheDir)
	f, readErr := readOverlayRaw(path)
	activity := transcriptActivity(r.TranscriptPath, now)
	merged, changed := mergeReading(f, r, activity, now)
	if readErr != nil {
		changed = true // replace the unusable file
	}
	if !changed {
		return false, nil
	}
	if err := writeOverlayRaw(path, merged); err != nil {
		return false, fmt.Errorf("write statusline overlay: %w", err)
	}
	return true, nil
}

func (s *storedBucket) toOverlay() *OverlayBucket {
	if s == nil {
		return nil
	}
	return &OverlayBucket{Bucket: *s.toBucket(), AsOf: s.AsOf, AcceptedAt: s.AcceptedAt}
}

// ReadOverlayFile returns the stored overlay as on disk, expired buckets
// included. A missing file is an empty overlay with a nil error; a non-nil
// error means the file exists but is unusable (for `doctor`).
func ReadOverlayFile(cacheDir string) (Overlay, error) {
	f, err := readOverlayRaw(overlayPath(cacheDir))
	if err != nil {
		return Overlay{}, err
	}
	return Overlay{FiveHour: f.FiveHour.toOverlay(), SevenDay: f.SevenDay.toOverlay()}, nil
}

// ReadOverlay returns the usable overlay at now: an unusable file fails open
// as empty, and a bucket whose reset has passed is dropped, so a passed
// window never displays from the overlay.
func ReadOverlay(cacheDir string, now time.Time) Overlay {
	ov, err := ReadOverlayFile(cacheDir)
	if err != nil {
		slog.Debug("anthro.statuslineOverlay ignored", "err", errWithoutPath(err))
		return Overlay{}
	}
	live := func(b *OverlayBucket) *OverlayBucket {
		if b == nil || !b.ResetsAt.After(now) {
			return nil
		}
		return b
	}
	return Overlay{FiveHour: live(ov.FiveHour), SevenDay: live(ov.SevenDay)}
}

// errWithoutPath strips a *fs.PathError down to its cause so a log line never
// carries the cache path.
func errWithoutPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// History knobs (spec §6). A statusline reading reaches usage_samples only
// once it has settled: held as the stored winner for statuslineSampleHold,
// with the last recorded statusline row at least statuslineSampleSpacing
// old. A leading-edge throttle would persist a transient value of a burst
// forever and drop the correct one that follows; this records the burst's
// final value on a later render from any session.
const (
	statuslineSampleHold    = 30 * time.Second
	statuslineSampleSpacing = 60 * time.Second
)

// PendingStatuslineSample reports the stored 5h/7d reading when it is due
// for recording to history. u holds only FiveHour/SevenDay (buckets whose
// reset has passed are excluded); at is the latest AcceptedAt among them —
// the row's timestamp. ok is false when there is nothing to record: no
// usable overlay, a reading not yet held statuslineSampleHold, a reading
// equal to the last one recorded, or a last row younger than
// statuslineSampleSpacing.
func PendingStatuslineSample(cacheDir string, now time.Time) (u Usage, at time.Time, ok bool) {
	f, err := readOverlayRaw(overlayPath(cacheDir))
	if err != nil {
		return Usage{}, time.Time{}, false
	}
	five, seven := f.FiveHour.liveAt(now), f.SevenDay.liveAt(now)
	if five == nil && seven == nil {
		return Usage{}, time.Time{}, false
	}
	at = five.acceptedAt()
	if t := seven.acceptedAt(); t.After(at) {
		at = t
	}
	if now.Sub(at) < statuslineSampleHold || f.Recorded.covers(five, seven, now) {
		return Usage{}, time.Time{}, false
	}
	return Usage{FiveHour: five.toBucket(), SevenDay: seven.toBucket()}, at, true
}

// liveAt is s, or nil when s is absent or its reset has passed at now.
func (s *storedBucket) liveAt(now time.Time) *storedBucket {
	if s == nil || !s.ResetsAt.After(now) {
		return nil
	}
	return s
}

func (s *storedBucket) acceptedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.AcceptedAt
}

func (s *storedBucket) reported() *reportedBucket {
	if s == nil {
		return nil
	}
	return &s.reportedBucket
}

// covers reports whether the last recorded sample rules out recording the
// stored pair now: it already holds the same values, or it is younger than
// statuslineSampleSpacing.
func (rec *recordedSample) covers(five, seven *storedBucket, now time.Time) bool {
	if rec == nil {
		return false
	}
	if sameReport(rec.FiveHour, five.reported()) && sameReport(rec.SevenDay, seven.reported()) {
		return true
	}
	return now.Sub(rec.At) < statuslineSampleSpacing
}

// MarkStatuslineRecorded notes, under the overlay lock, that the reading u
// was recorded to history at at, so PendingStatuslineSample stops offering
// it. Racing processes that both record the same reading are harmless: the
// same at is the same usage_samples ts, and the insert is OR IGNORE.
func MarkStatuslineRecorded(cacheDir string, u Usage, at time.Time) error {
	if release, lockErr := acquireFileLock(cacheDir, overlayLockName); lockErr == nil {
		defer release()
	}
	path := overlayPath(cacheDir)
	f, err := readOverlayRaw(path)
	if err != nil {
		return fmt.Errorf("mark statusline sample: %w", errWithoutPath(err))
	}
	f.V = overlayVersion
	f.Recorded = &recordedSample{At: at.UTC(), FiveHour: toReported(u.FiveHour), SevenDay: toReported(u.SevenDay)}
	if err := writeOverlayRaw(path, f); err != nil {
		return fmt.Errorf("mark statusline sample: %w", err)
	}
	return nil
}

func (s *storedBucket) toBucket() *Bucket {
	if s == nil {
		return nil
	}
	at := s.ResetsAt
	return &Bucket{Utilization: s.Utilization, ResetsAt: &at}
}
