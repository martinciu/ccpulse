package anthro

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Plausibility slack for a statusline bucket's resets_at (#568). A 5h window
// can never reset more than five hours out and a 7d window more than seven
// days out; the slack absorbs clock skew and the headers' rounding. Anything
// further out is a unit mix-up (milliseconds) or garbage, and accepting it
// would pin a bar until a date that never comes.
const (
	fiveHourWindow     = 5 * time.Hour
	sevenDayWindow     = 7 * 24 * time.Hour
	fiveHourResetSlack = 10 * time.Minute
	sevenDayResetSlack = time.Hour
)

// ErrNoStatuslineQuota reports a statusline payload that decoded fine but
// carries no 5h/7d bucket worth ingesting: rate_limits absent or null (not a
// subscriber, or no API response yet), only a gateway spend_limit, or every
// bucket rejected by validation.
var ErrNoStatuslineQuota = errors.New("statusline payload carries no usable rate_limits")

// StatuslineReading is the part of Claude Code's statusline stdin JSON that
// ccpulse ingests. FiveHour/SevenDay are nil when the bucket is absent or was
// rejected; a non-nil bucket always carries a non-nil ResetsAt in UTC.
//
// SessionID and TranscriptPath identify the reporting session for the
// overlay's freshness decision. They are personal data (the path reveals the
// home directory and project layout): never log them.
type StatuslineReading struct {
	SessionID      string
	TranscriptPath string
	FiveHour       *Bucket
	SevenDay       *Bucket
}

// flexFloat decodes a JSON number or a numeric string. Anything else — null,
// a non-numeric string, an object — leaves ok false instead of failing the
// decode, so one malformed field rejects its bucket rather than the payload.
type flexFloat struct {
	v  float64
	ok bool
}

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil //nolint:nilerr // lenient by design: a bad value rejects its bucket, not the payload
		}
		b = []byte(strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return nil //nolint:nilerr // lenient by design: see above
	}
	f.v, f.ok = v, true
	return nil
}

type statuslineBucket struct {
	UsedPercentage flexFloat `json:"used_percentage"`
	ResetsAt       flexFloat `json:"resets_at"`
}

// statuslinePayload is the decode target. Buckets stay raw so a bucket of
// the wrong JSON type rejects that bucket only; unknown fields are ignored.
type statuslinePayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	RateLimits     *struct {
		FiveHour json.RawMessage `json:"five_hour"`
		SevenDay json.RawMessage `json:"seven_day"`
	} `json:"rate_limits"`
}

// ParseStatusline decodes and validates a statusline payload at now.
//
// Per bucket, a reading is rejected when used_percentage is missing, NaN,
// infinite or negative, or when resets_at is missing, not after now, or
// beyond the bucket's window plus slack. Values above 100 are kept: an
// at-limit reading can exceed 100, and rejecting it would freeze the bar
// exactly when the user hits the limit (display clamps to 100).
//
// Invalid JSON returns a wrapped decode error; a payload with no usable
// bucket returns ErrNoStatuslineQuota. Neither error carries payload bytes
// worth logging — callers log only the outcome.
func ParseStatusline(b []byte, now time.Time) (StatuslineReading, error) {
	var p statuslinePayload
	if err := json.Unmarshal(b, &p); err != nil {
		return StatuslineReading{}, fmt.Errorf("decode statusline payload: %w", err)
	}
	r := StatuslineReading{SessionID: p.SessionID, TranscriptPath: p.TranscriptPath}
	if p.RateLimits != nil {
		r.FiveHour = parseStatuslineBucket(p.RateLimits.FiveHour, now, fiveHourWindow+fiveHourResetSlack)
		r.SevenDay = parseStatuslineBucket(p.RateLimits.SevenDay, now, sevenDayWindow+sevenDayResetSlack)
	}
	if r.FiveHour == nil && r.SevenDay == nil {
		return StatuslineReading{}, ErrNoStatuslineQuota
	}
	return r, nil
}

// parseStatuslineBucket validates one bucket; nil means absent or rejected.
// maxAhead bounds how far past now its reset may lie.
func parseStatuslineBucket(raw json.RawMessage, now time.Time, maxAhead time.Duration) *Bucket {
	if len(raw) == 0 {
		return nil
	}
	var sb statuslineBucket
	if err := json.Unmarshal(raw, &sb); err != nil {
		return nil
	}
	used, resets := sb.UsedPercentage, sb.ResetsAt
	if !used.ok || math.IsNaN(used.v) || math.IsInf(used.v, 0) || used.v < 0 {
		return nil
	}
	// Compare in float seconds before converting, so an absurd epoch can
	// never overflow the int64 conversion below.
	lo := float64(now.UnixNano()) / 1e9
	hi := float64(now.Add(maxAhead).UnixNano()) / 1e9
	if !resets.ok || math.IsNaN(resets.v) || resets.v <= lo || resets.v > hi {
		return nil
	}
	sec, frac := math.Modf(resets.v)
	at := time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
	return &Bucket{Utilization: used.v, ResetsAt: &at}
}

// transcriptActivity is the session's latest transcript write: the newest
// mtime among transcriptPath and every <transcriptPath sans .jsonl>/subagents/
// *.jsonl (subagent turns never touch the main transcript), clamped to now.
// Zero when the path is empty or the main transcript can't be stat'ed.
//
// Files are only stat'ed and the sibling directory only listed — nothing is
// opened or read. The path comes from untrusted stdin and is personal data:
// callers must not log it or any error that wraps it.
func transcriptActivity(transcriptPath string, now time.Time) time.Time {
	if transcriptPath == "" {
		return time.Time{}
	}
	fi, err := os.Stat(transcriptPath)
	if err != nil || !fi.Mode().IsRegular() {
		return time.Time{}
	}
	latest := fi.ModTime()
	subDir := filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "subagents")
	if entries, err := os.ReadDir(subDir); err == nil {
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if m := info.ModTime(); m.After(latest) {
				latest = m
			}
		}
	}
	if latest.After(now) {
		return now
	}
	return latest
}
