package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/parse"
)

func TestAppendParseErrorsFormatsLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")

	AppendParseErrors(logPath, "/some/file.jsonl", []parse.ParseError{
		{Line: 12, Err: errors.New("bad json")},
		{Line: 13, Err: errors.New("more bad json")},
	})

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "/some/file.jsonl:12 bad json\n/some/file.jsonl:13 more bad json\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestAppendParseErrorsRotatesAt10MB(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")

	// Pre-fill with > 10 MB.
	big := strings.Repeat("x", 11*1024*1024)
	if err := os.WriteFile(logPath, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}

	AppendParseErrors(logPath, "/x.jsonl", []parse.ParseError{
		{Line: 1, Err: errors.New("oops")},
	})

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > int64(len("/x.jsonl:1 oops\n"))+10 {
		t.Errorf("log not rotated: size = %d", info.Size())
	}
}

func TestLogFileErrorWritesSingleLine(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")

	LogFileError(logPath, "/missing.jsonl", errors.New("no such file"))

	got, _ := os.ReadFile(logPath)
	want := "/missing.jsonl: no such file\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}
