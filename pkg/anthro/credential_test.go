package anthro

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeCred(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCredentialFromFile(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "claudeAiOauth": {
	    "accessToken": "tok-abc",
	    "refreshToken": "ref-xyz",
	    "expiresAt": 1778358665917,
	    "scopes": ["user:profile", "user:inference"],
	    "subscriptionType": "max",
	    "rateLimitTier": "default_claude_max_20x"
	  }
	}`
	p := writeCred(t, dir, body)
	c, err := LoadCredentialFromFile(p)
	if err != nil {
		t.Fatalf("LoadCredentialFromFile: %v", err)
	}
	if c.AccessToken != "tok-abc" {
		t.Errorf("AccessToken = %q", c.AccessToken)
	}
	if c.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("RateLimitTier = %q", c.RateLimitTier)
	}
	if c.SubscriptionType != "max" {
		t.Errorf("SubscriptionType = %q", c.SubscriptionType)
	}
	want := time.UnixMilli(1778358665917)
	if !c.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", c.ExpiresAt, want)
	}
	if len(c.Scopes) != 2 {
		t.Errorf("Scopes = %v", c.Scopes)
	}
}

func TestLoadCredentialMissingFile(t *testing.T) {
	_, err := LoadCredentialFromFile(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("missing file: got %v, want ErrNoCredential", err)
	}
}

func TestLoadCredentialEmptyToken(t *testing.T) {
	dir := t.TempDir()
	p := writeCred(t, dir, `{"claudeAiOauth":{"accessToken":""}}`)
	_, err := LoadCredentialFromFile(p)
	if !errors.Is(err, ErrNoCredential) {
		t.Errorf("empty token: got %v, want ErrNoCredential", err)
	}
}

func TestLoadCredentialBadJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeCred(t, dir, `not json`)
	_, err := LoadCredentialFromFile(p)
	if err == nil {
		t.Errorf("bad JSON: want error")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Errorf("bad JSON shouldn't be ErrNoCredential, got %v", err)
	}
}

func TestLoadCredentialMissingSubscriptionType(t *testing.T) {
	dir := t.TempDir()
	p := writeCred(t, dir, `{"claudeAiOauth":{"accessToken":"tok-abc"}}`)
	_, err := LoadCredentialFromFile(p)
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Errorf("missing subscriptionType shouldn't be ErrNoCredential, got %v", err)
	}
}

func TestLoadCredentialFromKeychainPinsAccount(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain only on darwin")
	}
	var gotArgs []string
	prev := keychainExec
	keychainExec = func(name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(`{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"max"}}`), nil
	}
	t.Cleanup(func() { keychainExec = prev })

	if _, err := loadCredentialFromKeychain(); err != nil {
		t.Fatalf("loadCredentialFromKeychain: %v", err)
	}
	idx := indexOfArg(gotArgs, "-a")
	if idx < 0 {
		t.Fatalf("missing -a in args: %v", gotArgs)
	}
	if idx+1 >= len(gotArgs) {
		t.Fatalf("no value after -a: %v", gotArgs)
	}
	if gotArgs[idx+1] == "" {
		t.Errorf("-a value is empty: %v", gotArgs)
	}
}

func indexOfArg(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

func TestExpired(t *testing.T) {
	c := Credential{ExpiresAt: time.Now().Add(-time.Hour)}
	if !c.Expired(time.Now()) {
		t.Errorf("expected expired")
	}
	c2 := Credential{ExpiresAt: time.Now().Add(time.Hour)}
	if c2.Expired(time.Now()) {
		t.Errorf("expected not expired")
	}
	c3 := Credential{}
	if c3.Expired(time.Now()) {
		t.Errorf("zero ExpiresAt should not be expired")
	}
}
