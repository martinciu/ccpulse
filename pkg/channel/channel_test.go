package channel

import "testing"

func TestDefaultIsDev(t *testing.T) {
	reset()
	t.Cleanup(reset)
	if !IsDev() {
		t.Errorf("default IsDev() = false, want true")
	}
	if got := Channel(); got != "dev" {
		t.Errorf("default Channel() = %q, want %q", got, "dev")
	}
}

func TestSetRelease(t *testing.T) {
	reset()
	t.Cleanup(reset)
	Set("release")
	if IsDev() {
		t.Errorf("after Set(\"release\"), IsDev() = true")
	}
	if got := Channel(); got != "release" {
		t.Errorf("Channel() = %q, want %q", got, "release")
	}
}

func TestSetUnknownNormalisesToDev(t *testing.T) {
	reset()
	t.Cleanup(reset)
	Set("garbage")
	if !IsDev() {
		t.Errorf("Set(\"garbage\") should normalise to dev, IsDev() = false")
	}
	if got := Channel(); got != "dev" {
		t.Errorf("Set(\"garbage\") Channel() = %q, want %q", got, "dev")
	}
}

func TestSetDevAfterRelease(t *testing.T) {
	reset()
	t.Cleanup(reset)
	Set("release")
	Set("dev")
	if !IsDev() {
		t.Errorf("Set(\"dev\") after release: IsDev() = false")
	}
}

// reset is a test helper — not exported. Keeps tests independent of order.
func reset() { current = "dev" }
