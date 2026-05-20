package tui

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// captureLogs swaps slog.Default for a slice-backed handler at the given
// level and restores it via t.Cleanup. Returns a snapshot getter. Mirrors
// the helper in pkg/anthro.
//
// Caveat: slog.SetDefault is process-global, so tests using captureLogs MUST
// NOT call t.Parallel().
func captureLogs(t *testing.T, minLevel slog.Level) func() []slog.Record {
	t.Helper()
	var (
		mu   sync.Mutex
		recs []slog.Record
	)
	prev := slog.Default()
	h := &captureHandler{level: minLevel, mu: &mu, recs: &recs}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []slog.Record {
		mu.Lock()
		defer mu.Unlock()
		snap := make([]slog.Record, len(recs))
		copy(snap, recs)
		return snap
	}
}

type captureHandler struct {
	level slog.Level
	mu    *sync.Mutex
	recs  *[]slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// attrMap collects a record's attributes into a key->value map.
func attrMap(r slog.Record) map[string]any {
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	return m
}
