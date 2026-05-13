package devlog

import (
	"bytes"
	"log/slog"
	"testing"
)

// TestPrivacy_NoSensitiveTokensInSlogOutput is a CI guard: if a future
// contribution adds a slog call site that embeds a credential token, this
// test fails before the change lands. The sensitive-string list mirrors the
// privacy review checklist in issue #138.
func TestPrivacy_NoSensitiveTokensInSlogOutput(t *testing.T) {
	prevSlog := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevSlog) })

	dir := t.TempDir()
	closer, err := Init(dir, "info")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	// Redirect slog output to a buffer so we can inspect what Init and
	// subsequent calls would write.
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))

	slog.Debug("debug event")
	slog.Info("info event")
	slog.Warn("warn event")
	slog.Error("error event")

	output := buf.String()
	sensitive := []string{
		"sk-ant-",
		"Bearer ",
		"sk-ant-api",
	}
	for _, token := range sensitive {
		if bytes.Contains([]byte(output), []byte(token)) {
			t.Errorf("slog output contains sensitive token %q:\n%s", token, output)
		}
	}
}
