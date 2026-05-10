package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	want := `"/some/file.jsonl":12 "bad json"` + "\n" +
		`"/some/file.jsonl":13 "more bad json"` + "\n"
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
	// New format: `"/x.jsonl":1 "oops"\n` — much smaller than 10 MB.
	if info.Size() > 1024 {
		t.Errorf("log not rotated: size = %d", info.Size())
	}
}

func TestLogFileErrorWritesSingleLine(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")

	LogFileError(logPath, "/missing.jsonl", errors.New("no such file"))

	got, _ := os.ReadFile(logPath)
	want := `"/missing.jsonl": "no such file"` + "\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestAppendParseErrors_SanitizesContent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")
	perrs := []parse.ParseError{
		{Line: 1, Err: errors.New("\x1b[31mboom\x1b[0m\nINJECTED\n")},
	}
	AppendParseErrors(logPath, "src.jsonl", perrs)
	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(out)
	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("want 1 newline, got %d in %q", n, got)
	}
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("raw escape leaked: %q", got)
	}
	if !strings.Contains(got, `\x1b[31mboom\x1b[0m`) {
		t.Fatalf("escape not literalized: %q", got)
	}
	if !strings.Contains(got, `INJECTED\n`) {
		t.Fatalf("embedded newline not literalized: %q", got)
	}
}

func TestAppendParseErrors_SanitizesSourcePath(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")
	perrs := []parse.ParseError{{Line: 1, Err: errors.New("ok")}}
	AppendParseErrors(logPath, "evil\x1b[2J.jsonl", perrs)
	out, _ := os.ReadFile(logPath)
	got := string(out)
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("raw escape in source leaked: %q", got)
	}
	if !strings.Contains(got, `evil\x1b[2J.jsonl`) {
		t.Fatalf("source not literalized: %q", got)
	}
}

func TestAppendParseErrors_OpensLogOncePerCall(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")
	var n int64
	prev := openLogFile
	openLogFile = func(path string) (*os.File, error) {
		atomic.AddInt64(&n, 1)
		return prev(path)
	}
	defer func() { openLogFile = prev }()

	perrs := make([]parse.ParseError, 10)
	for i := range perrs {
		perrs[i] = parse.ParseError{Line: i, Err: errors.New("e")}
	}
	AppendParseErrors(logPath, "src.jsonl", perrs)
	if got := atomic.LoadInt64(&n); got != 1 {
		t.Fatalf("openLogFile calls: got %d want 1", got)
	}
}

func TestAppendParseErrors_FileMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")
	AppendParseErrors(logPath, "src.jsonl", []parse.ParseError{{Line: 1, Err: errors.New("e")}})
	if got, _ := os.Stat(logPath); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

func TestAppendParseErrors_TightensExisting(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "parse-errors.log")
	if err := os.WriteFile(logPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	AppendParseErrors(logPath, "src.jsonl", []parse.ParseError{{Line: 1, Err: errors.New("e")}})
	if got, _ := os.Stat(logPath); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}
