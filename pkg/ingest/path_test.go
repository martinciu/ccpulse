package ingest

import "testing"

func TestSlugAndSubagent_TopLevel(t *testing.T) {
	slug, isSub, parent := SlugAndSubagent("/projects", "/projects/-Users-x-foo/sess.jsonl")
	if slug != "-Users-x-foo" {
		t.Errorf("slug = %q, want -Users-x-foo", slug)
	}
	if isSub {
		t.Errorf("isSub = true, want false")
	}
	if parent != "" {
		t.Errorf("parent = %q, want empty", parent)
	}
}

func TestSlugAndSubagent_Subagent(t *testing.T) {
	slug, isSub, parent := SlugAndSubagent(
		"/projects",
		"/projects/-Users-x-foo/sid-abc/subagents/agent-1.jsonl",
	)
	if slug != "-Users-x-foo" {
		t.Errorf("slug = %q, want -Users-x-foo", slug)
	}
	if !isSub {
		t.Errorf("isSub = false, want true")
	}
	if parent != "sid-abc" {
		t.Errorf("parent = %q, want sid-abc", parent)
	}
}

func TestSlugAndSubagent_OutsideRoot(t *testing.T) {
	slug, isSub, parent := SlugAndSubagent("/projects", "/elsewhere/foo.jsonl")
	if slug != "" || isSub || parent != "" {
		t.Errorf("got slug=%q isSub=%v parent=%q, want all empty/false",
			slug, isSub, parent)
	}
}

func TestSlugAndSubagent_RootItself(t *testing.T) {
	slug, isSub, parent := SlugAndSubagent("/projects", "/projects")
	if slug != "" || isSub || parent != "" {
		t.Errorf("root path should yield empty slug")
	}
}
