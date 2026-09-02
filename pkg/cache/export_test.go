package cache

// RecostFingerprint exposes the unexported recostFingerprint for tests in the
// external cache_test package, so tests assert against the real
// implementation instead of hand-building the fingerprint string.
var RecostFingerprint = recostFingerprint
