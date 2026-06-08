package cache

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSchema_HasRepoRoot(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var n int
	if err := c.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='repo_root'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages.repo_root column count = %d, want 1", n)
	}

	var idx int
	if err := c.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_ts_repo_root'`).
		Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatalf("idx_messages_ts_repo_root count = %d, want 1", idx)
	}

	if SchemaVersion != "8" {
		t.Fatalf("SchemaVersion = %q, want 8", SchemaVersion)
	}
}
