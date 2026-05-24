package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexRebuildSmoke(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects")
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(projects, "-fake-slug"), 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := []byte(`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}},"sessionId":"s","timestamp":"2026-05-09T10:00:00.000Z"}` + "\n")
	if err := os.WriteFile(filepath.Join(projects, "-fake-slug", "x.jsonl"), jsonl, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CCPULSE_PROJECTS_ROOT", projects)
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)

	root := newRootCmd()
	root.SetArgs([]string{"index", "--rebuild"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(cacheDir, "state.db")); err != nil {
		t.Fatalf("cache db not created: %v", err)
	}
}

func TestBareIndexReturnsRemovalError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPULSE_PROJECTS_ROOT", filepath.Join(dir, "projects"))
	t.Setenv("CCPULSE_CACHE_DIR", filepath.Join(dir, "cache"))

	root := newRootCmd()
	root.SetArgs([]string{"index"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected `ccpulse index` (no flag) to error")
	}
	if !strings.Contains(err.Error(), "--rebuild") {
		t.Errorf("error message should mention --rebuild, got: %v", err)
	}
}
