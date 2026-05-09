package parse

import (
	"sort"
	"testing"
)

func TestWalkProjects(t *testing.T) {
	msgs, err := WalkProjects("testdata/walkdir")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2", len(msgs))
	}
	// Sort by InputTokens so order is stable: main=11, subagent=22.
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].InputTokens < msgs[j].InputTokens })

	main, sub := msgs[0], msgs[1]

	if main.IsSubagent {
		t.Errorf("main message marked as subagent")
	}
	if main.ProjectSlug != "-Users-x-foo" {
		t.Errorf("main.ProjectSlug = %q, want -Users-x-foo", main.ProjectSlug)
	}

	if !sub.IsSubagent {
		t.Errorf("subagent message NOT marked as subagent")
	}
	if sub.ParentSessionID != "sid-abc" {
		t.Errorf("sub.ParentSessionID = %q, want sid-abc", sub.ParentSessionID)
	}
	if sub.ProjectSlug != "-Users-x-foo" {
		t.Errorf("sub.ProjectSlug = %q, want -Users-x-foo", sub.ProjectSlug)
	}
}
