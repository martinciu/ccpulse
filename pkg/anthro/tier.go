// Package anthro talks to api.anthropic.com on behalf of ccpulse, using the
// Claude Code OAuth credential. Pure helpers (tier mapping) live alongside
// the network code so callers have one import.
package anthro

import "strings"

// TierSlug returns a stable, kebab-case label for status --json.
// Forward-compat: any default_claude_<x> passes <x> through verbatim.
func TierSlug(rateLimitTier string) string {
	if rateLimitTier == "" {
		return "unknown"
	}
	if s, ok := strings.CutPrefix(rateLimitTier, "default_claude_"); ok {
		return s
	}
	return rateLimitTier
}

// TierPretty returns a human-readable label for the TUI / tmux line.
func TierPretty(rateLimitTier string) string {
	slug := TierSlug(rateLimitTier)
	switch slug {
	case "unknown":
		return "Unknown"
	case "pro":
		return "Pro"
	}
	if s, ok := strings.CutPrefix(slug, "max_"); ok {
		return "Max " + s
	}
	// fallback: "weird_value" → "Weird Value"
	parts := strings.Split(slug, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
