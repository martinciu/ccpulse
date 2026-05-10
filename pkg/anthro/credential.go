package anthro

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoCredential signals "no usable credential found" — not a parse error.
// Callers should treat this as the OAuth-less mode (API user).
var ErrNoCredential = errors.New("anthro: no credential")

// Credential is the parsed OAuth credential as exposed by `claude` CLI.
type Credential struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	Scopes           []string
	SubscriptionType string
	RateLimitTier    string
}

// Expired returns true when ExpiresAt is non-zero and in the past.
// A zero ExpiresAt means the credential file omitted the field; treat
// as not expired so we still try the API.
func (c Credential) Expired(now time.Time) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return now.After(c.ExpiresAt)
}

// LoadCredential resolves the credential from the platform's preferred store.
// Darwin: macOS Keychain (service "Claude Code-credentials"), with the file
// at ~/.claude/.credentials.json as fallback. Other platforms: file only.
func LoadCredential() (Credential, error) {
	if runtime.GOOS == "darwin" {
		if c, err := loadCredentialFromKeychain(); err == nil {
			return c, nil
		}
		// fall through to file
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Credential{}, fmt.Errorf("home dir: %w", err)
	}
	return LoadCredentialFromFile(filepath.Join(home, ".claude", ".credentials.json"))
}

// LoadCredentialFromFile parses the credential JSON at path.
// Missing file → ErrNoCredential. Empty access token → ErrNoCredential.
// Invalid JSON → wrapped error (not ErrNoCredential — that's a real fault).
func LoadCredentialFromFile(path string) (Credential, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credential{}, ErrNoCredential
		}
		return Credential{}, fmt.Errorf("read %s: %w", path, err)
	}
	return parseCredential(b)
}

func loadCredentialFromKeychain() (Credential, error) {
	out, err := exec.Command("security",
		"find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return Credential{}, ErrNoCredential
	}
	return parseCredential([]byte(strings.TrimSpace(string(out))))
}

type credEnvelope struct {
	ClaudeAiOauth struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        int64    `json:"expiresAt"` // unix-ms
		Scopes           []string `json:"scopes"`
		SubscriptionType string   `json:"subscriptionType"`
		RateLimitTier    string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

func parseCredential(b []byte) (Credential, error) {
	var env credEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Credential{}, fmt.Errorf("parse credential: %w", err)
	}
	o := env.ClaudeAiOauth
	if o.AccessToken == "" {
		return Credential{}, ErrNoCredential
	}
	c := Credential{
		AccessToken:      o.AccessToken,
		RefreshToken:     o.RefreshToken,
		Scopes:           o.Scopes,
		SubscriptionType: o.SubscriptionType,
		RateLimitTier:    o.RateLimitTier,
	}
	if o.ExpiresAt > 0 {
		c.ExpiresAt = time.UnixMilli(o.ExpiresAt)
	}
	return c, nil
}
