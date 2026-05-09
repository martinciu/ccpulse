package ingest

import (
	"os"
	"path/filepath"
	"strings"
)

// SlugAndSubagent classifies a .jsonl path under projectsRoot.
// Returns the top-level slug (the directory directly under root),
// whether the file is a subagent transcript, and (if so) the
// parent session id.
//
// Subagent transcripts live at:
//
//	<root>/<slug>/<parent-session-id>/subagents/agent-*.jsonl
//
// Returns ("", false, "") if path is not under root or has no
// recognisable slug component.
func SlugAndSubagent(root, path string) (slug string, isSubagent bool, parentSID string) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false, ""
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 0 || parts[0] == "" {
		return "", false, ""
	}
	slug = parts[0]
	if len(parts) >= 4 && parts[2] == "subagents" {
		isSubagent = true
		parentSID = parts[1]
	}
	return slug, isSubagent, parentSID
}
