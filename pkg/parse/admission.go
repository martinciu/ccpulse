package parse

import (
	"errors"
	"strings"
)

// Admission tallies the admission decision for every complete assistant line in a file.
type Admission struct {
	Admitted int // assistant lines that produced a message (iteration rows not double-counted)
	Refused  int // missing envelope, unusable timestamp, or undecodable assistant line
}

// ScanAdmission parses the whole file at path and counts which assistant lines
// were admitted and which were refused. It is the census `ccpulse doctor` sums
// to grade the refusal rate (#532).
//
// It shares ParseFromOffsetWithErrors with ingest, so the decision counted here
// is exactly the one that decides what reaches the cache, and an in-progress
// (unterminated) last line is never counted. Which ParseErrors count as
// refusals is decided here, next to the sentinels, so a future refusal class
// touches only this package. Oversized lines and JSON too broken to attribute
// are not admission decisions and are not counted.
//
// Admitted counts parent rows only: an assistant line whose usage.iterations
// expands into ":it:" attempt rows still counts once, so iteration expansion
// cannot inflate the denominator and mask a spike of refusals.
//
// An open, seek or read error is returned as-is with a zero Admission.
func ScanAdmission(path string) (Admission, error) {
	msgs, errs, _, _, err := ParseFromOffsetWithErrors(path, "", 0, 0)
	if err != nil {
		return Admission{}, err
	}
	var a Admission
	for _, m := range msgs {
		if !strings.Contains(m.MessageID, attemptKeySep) {
			a.Admitted++
		}
	}
	for _, pe := range errs {
		if isRefusal(pe.Err) {
			a.Refused++
		}
	}
	return a, nil
}

// isRefusal reports whether a ParseError cause is an admission refusal of an
// assistant line, as opposed to a line that was never decided on.
func isRefusal(err error) bool {
	return errors.Is(err, ErrMissingEnvelope) ||
		errors.Is(err, ErrZeroTimestamp) ||
		errors.Is(err, ErrUndecodableAssistant)
}
