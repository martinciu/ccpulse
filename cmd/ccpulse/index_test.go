package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexCmdSmoke(t *testing.T) {
	// Lay out a fake projects root and cache dir.
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(projects, "-fake-slug"), 0755); err != nil {
		t.Fatal(err)
	}
	jsonl := []byte(`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}},"sessionId":"s","timestamp":"2026-05-09T10:00:00.000Z"}` + "\n")
	if err := os.WriteFile(filepath.Join(projects, "-fake-slug", "x.jsonl"), jsonl, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CCPULSE_PROJECTS_ROOT", projects)
	t.Setenv("CCPULSE_CACHE_DIR", cache)

	root := newRootCmd()
	root.SetArgs([]string{"index"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	// Verify the cache exists
	if _, err := os.Stat(filepath.Join(cache, "state.db")); err != nil {
		t.Fatalf("cache db not created: %v", err)
	}
}
