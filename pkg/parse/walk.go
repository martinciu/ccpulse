package parse

import (
	"os"
	"path/filepath"
	"strings"
)

// WalkProjects walks the projects root, parses every *.jsonl, and
// returns all assistant messages. Subagent transcripts (under
// <slug>/<session-id>/subagents/agent-*.jsonl) are tagged
// IsSubagent=true with ParentSessionID set to the directory name.
func WalkProjects(root string) ([]Message, error) {
	var all []Message
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) < 2 {
			return nil
		}
		slug := parts[0]
		isSub := len(parts) >= 4 && parts[2] == "subagents"
		parentSID := ""
		if isSub {
			parentSID = parts[1]
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		msgs, err := Parse(f, slug)
		if err != nil {
			return err
		}
		for i := range msgs {
			msgs[i].IsSubagent = isSub
			msgs[i].ParentSessionID = parentSID
		}
		all = append(all, msgs...)
		return nil
	})
	return all, err
}
