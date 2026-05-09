// Package anthro fetches live usage data from the Anthropic API
// (api.anthropic.com/api/oauth/usage) so ccpulse can show the real
// server-side 5-hour block reset time instead of inferring it from
// JSONL timestamps.
package anthro

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	cacheTTL = 3 * time.Minute
	apiURL   = "https://api.anthropic.com/api/oauth/usage"
	keySvc   = "Claude Code-credentials"
)

// UsageData holds the fields ccpulse cares about from the usage API.
type UsageData struct {
	SessionResetAt time.Time
	Pct            int // 0-100; 0 means unknown
}

// Fetch returns Anthropic's live 5-hour block data.
// Resolution order:
//  1. ccstatusline cache (~/.cache/ccstatusline/usage.json) if < 3 min old
//  2. ccpulse's own cache (cacheDir/usage.json) if < 3 min old
//  3. Live API call (writes result to ccpulse cache)
//
// Returns an error only if all three sources fail.
func Fetch(cacheDir string) (UsageData, error) {
	if d, ok := ccStatuslineCache(); ok {
		return d, nil
	}
	ours := filepath.Join(cacheDir, "usage.json")
	if d, ok := ourCache(ours); ok {
		return d, nil
	}
	tok, err := getToken()
	if err != nil {
		return UsageData{}, err
	}
	d, err := apiCall(tok)
	if err != nil {
		return UsageData{}, err
	}
	writeOurCache(ours, d)
	return d, nil
}

// --- credential sources ---

type credJSON struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

func getToken() (string, error) {
	if runtime.GOOS == "darwin" {
		if tok := keychainToken(); tok != "" {
			return tok, nil
		}
	}
	return fileToken()
}

func keychainToken() string {
	b, err := exec.Command("security", "find-generic-password", "-s", keySvc, "-w").Output()
	if err != nil {
		return ""
	}
	var c credJSON
	if json.Unmarshal([]byte(strings.TrimSpace(string(b))), &c) != nil {
		return ""
	}
	return c.ClaudeAiOauth.AccessToken
}

func fileToken() (string, error) {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return "", fmt.Errorf("credentials file: %w", err)
	}
	var c credJSON
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	if c.ClaudeAiOauth.AccessToken == "" {
		return "", fmt.Errorf("empty access token in credentials file")
	}
	return c.ClaudeAiOauth.AccessToken, nil
}

// --- live API ---

type apiResp struct {
	FiveHour *struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"five_hour"`
}

func apiCall(tok string) (UsageData, error) {
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return UsageData{}, err
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return UsageData{}, err
	}
	if r.FiveHour == nil {
		return UsageData{}, fmt.Errorf("no five_hour in API response")
	}
	t, err := time.Parse(time.RFC3339Nano, r.FiveHour.ResetsAt)
	if err != nil {
		return UsageData{}, fmt.Errorf("parse resets_at: %w", err)
	}
	// utilization is 0.0–1.0 for the five_hour block
	pct := int(r.FiveHour.Utilization * 100)
	if pct > 100 {
		pct = 100
	}
	return UsageData{SessionResetAt: t.UTC(), Pct: pct}, nil
}

// --- caches ---

type ccslCacheFile struct {
	SessionResetAt string `json:"sessionResetAt"`
}

func ccStatuslineCache() (UsageData, bool) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".cache", "ccstatusline", "usage.json")
	info, err := os.Stat(p)
	if err != nil || time.Since(info.ModTime()) > cacheTTL {
		return UsageData{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return UsageData{}, false
	}
	var c ccslCacheFile
	if err := json.Unmarshal(b, &c); err != nil || c.SessionResetAt == "" {
		return UsageData{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, c.SessionResetAt)
	if err != nil {
		return UsageData{}, false
	}
	return UsageData{SessionResetAt: t.UTC()}, true
}

type ourCacheFile struct {
	SessionResetAt string `json:"session_reset_at"`
	Pct            int    `json:"pct"`
}

func ourCache(path string) (UsageData, bool) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > cacheTTL {
		return UsageData{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return UsageData{}, false
	}
	var c ourCacheFile
	if err := json.Unmarshal(b, &c); err != nil || c.SessionResetAt == "" {
		return UsageData{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, c.SessionResetAt)
	if err != nil {
		return UsageData{}, false
	}
	return UsageData{SessionResetAt: t.UTC(), Pct: c.Pct}, true
}

func writeOurCache(path string, d UsageData) {
	b, _ := json.Marshal(ourCacheFile{
		SessionResetAt: d.SessionResetAt.Format(time.RFC3339Nano),
		Pct:            d.Pct,
	})
	_ = os.WriteFile(path, b, 0644)
}
