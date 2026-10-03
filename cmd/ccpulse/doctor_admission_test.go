package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	admissionPrefix = "transcript admission (7d): "
	admissionRemedy = " — Claude Code's transcript format may have changed and totals are under-counted; " +
		"parse-errors.log names the files. Upgrade ccpulse or file an issue."
)

// writeAdmissionTranscript writes good enveloped assistant lines followed by
// refused (#527-shape, envelope-less) ones to <root>/-slug/<name>.jsonl and
// ages the file to mtime.
func writeAdmissionTranscript(t *testing.T, root, name string, good, refused int, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, "-slug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := range good {
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":"s1","uuid":"u%d","timestamp":"2026-05-09T10:00:00.000Z",`+
			`"message":{"id":"m%d","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n", i, i)
	}
	for range refused {
		b.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}` + "\n")
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestReportAdmission(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{
			name:  "empty root",
			setup: func(*testing.T, string) {},
			want:  "ℹ " + admissionPrefix + "no assistant lines",
		},
		{
			name:  "clean",
			setup: func(t *testing.T, root string) { writeAdmissionTranscript(t, root, "a", 3, 0, now) },
			want:  "ℹ " + admissionPrefix + "0 of 3 assistant lines refused",
		},
		{
			// 0.83%: the minimum count is met, the rate is not.
			name:  "below rate",
			setup: func(t *testing.T, root string) { writeAdmissionTranscript(t, root, "a", 600, 5, now) },
			want:  "ℹ " + admissionPrefix + "5 of 605 assistant lines refused",
		},
		{
			// 100%, but four lines on a quiet week is not a format change.
			name:  "below minimum",
			setup: func(t *testing.T, root string) { writeAdmissionTranscript(t, root, "a", 0, 4, now) },
			want:  "ℹ " + admissionPrefix + "4 of 4 assistant lines refused",
		},
		{
			name:  "over threshold",
			setup: func(t *testing.T, root string) { writeAdmissionTranscript(t, root, "a", 5, 5, now) },
			want:  "✗ " + admissionPrefix + "5 of 10 assistant lines refused (50.0%)" + admissionRemedy,
		},
		{
			name:  "exact boundary",
			setup: func(t *testing.T, root string) { writeAdmissionTranscript(t, root, "a", 495, 5, now) },
			want:  "✗ " + admissionPrefix + "5 of 500 assistant lines refused (1.0%)" + admissionRemedy,
		},
		{
			name: "outside window",
			setup: func(t *testing.T, root string) {
				writeAdmissionTranscript(t, root, "old", 0, 10, now.Add(-8*24*time.Hour))
				writeAdmissionTranscript(t, root, "new", 1, 0, now)
			},
			want: "ℹ " + admissionPrefix + "0 of 1 assistant lines refused",
		},
		{
			name: "unreadable",
			setup: func(t *testing.T, root string) {
				writeAdmissionTranscript(t, root, "a", 1, 0, now)
				if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "-slug", "bad.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
			want: "ℹ " + admissionPrefix + "0 of 1 assistant lines refused (1 file unreadable)",
		},
		{
			name: "two unreadable over threshold",
			setup: func(t *testing.T, root string) {
				writeAdmissionTranscript(t, root, "a", 5, 5, now)
				for _, n := range []string{"bad1.jsonl", "bad2.jsonl"} {
					if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "-slug", n)); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "✗ " + admissionPrefix + "5 of 10 assistant lines refused (50.0%) (2 files unreadable)" + admissionRemedy,
		},
		{
			// A directory the walk cannot read is skipped, not fatal: the
			// rest of the tree is still counted.
			name: "unreadable directory skipped",
			setup: func(t *testing.T, root string) {
				writeAdmissionTranscript(t, root, "a", 1, 0, now)
				locked := filepath.Join(root, "-locked")
				if err := os.Mkdir(locked, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			},
			want: "ℹ " + admissionPrefix + "0 of 1 assistant lines refused",
		},
		{
			name: "ignored entries",
			setup: func(t *testing.T, root string) {
				writeAdmissionTranscript(t, root, "a", 1, 0, now)
				if err := os.Mkdir(filepath.Join(root, "-slug", "d.jsonl"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "-slug", "notes.txt"), []byte("not a transcript\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "ℹ " + admissionPrefix + "0 of 1 assistant lines refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tt.setup(t, root)
			var out bytes.Buffer
			reportAdmission(t.Context(), &out, root, now)
			if got := out.String(); got != tt.want+"\n" {
				t.Errorf("reportAdmission output:\n got %q\nwant %q", got, tt.want+"\n")
			}
		})
	}
}

// TestReportAdmission_Cancelled: Ctrl-C during the walk must print that the
// scan was interrupted, never a verdict computed from part of the window.
func TestReportAdmission_Cancelled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	now := time.Now()
	writeAdmissionTranscript(t, root, "a", 1, 0, now)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out bytes.Buffer
	reportAdmission(ctx, &out, root, now)
	if got, want := out.String(), "ℹ "+admissionPrefix+"scan interrupted\n"; got != want {
		t.Errorf("reportAdmission output = %q, want %q", got, want)
	}
}
