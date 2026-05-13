package anthro

// Privacy sentinels — recognisable strings planted in test fixtures so the
// privacy guard test can assert they never appear in slog output.
//
// The literal "PRIVACY-SENTINEL-138" in every value makes any leakage
// trivially greppable.
//
// Scope: these constants guard the *credential surface* — every string-
// shaped field on Credential. They do NOT cover response body bytes
// (which the current code intentionally logs in body_snippet on non-2xx
// and decode-error paths, bounded to maxBodySnippet and strconv.Quote'd
// per the privacy review in the design spec). If a slog call that handles
// full response payloads is added later, extend this set and the
// corresponding test in usage_test.go.
const (
	privacyAccessToken      = "Bearer-PRIVACY-SENTINEL-138-BEARER-VALUE"
	privacyRefreshToken     = "refresh-PRIVACY-SENTINEL-138-REFRESH-VALUE"
	privacySubscriptionType = "sub-PRIVACY-SENTINEL-138-SUBSCRIPTION"
	privacyRateLimitTier    = "tier-PRIVACY-SENTINEL-138-RATELIMIT"
	privacyScope            = "scope-PRIVACY-SENTINEL-138-SCOPE"
)
