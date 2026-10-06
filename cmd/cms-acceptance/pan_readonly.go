package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
)

type readAudit struct {
	At        time.Time `json:"at"`
	Operation string    `json:"operation"`
	Status    int       `json:"status,omitempty"`
	Blocked   bool      `json:"blocked,omitempty"`
}

// All login, logout, SSO, file mutations, uploads and CMS requests fail before
// reaching the network. The POST allowlist contains only the read-only downurl
// API; POST is required by that API even though it does not modify files.
type reuseOnlyTransport struct {
	base      http.RoundTripper
	mu        sync.Mutex
	maximum   int
	interval  time.Duration
	last      time.Time
	requests  int
	blocked   int
	auditPath string
}

func (t *reuseOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	op := "blocked"
	if r.URL.Scheme == "https" && r.URL.User == nil && r.URL.Port() == "" {
		switch {
		case r.Method == http.MethodGet && r.URL.Host == "my.115.com" && r.URL.Path == "/" && isStatusQuery(r):
			op = "cookie_status"
		case r.Method == http.MethodPost && r.URL.Host == "proapi.115.com" && r.URL.Path == "/app/chrome/downurl":
			op = "download_url"
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if op == "blocked" {
		t.blocked++
		t.audit(readAudit{At: time.Now().UTC(), Operation: "endpoint_rejected", Blocked: true})
		return nil, errors.New("request outside Cookie reuse read allowlist")
	}
	if t.requests >= t.maximum {
		return nil, errors.New("115 read request budget exhausted")
	}
	if delay := time.Until(t.last.Add(t.interval)); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return nil, r.Context().Err()
		case <-timer.C:
		}
	}
	t.requests++
	t.last = time.Now()
	resp, err := t.base.RoundTrip(r)
	entry := readAudit{At: time.Now().UTC(), Operation: op}
	if resp != nil {
		entry.Status = resp.StatusCode
	}
	t.audit(entry)
	if err != nil {
		return nil, errors.New("115 read connection failed")
	}
	return resp, nil
}

func isStatusQuery(r *http.Request) bool {
	query := r.URL.Query()
	if len(query["ct"]) != 1 || len(query["ac"]) != 1 || query.Get("ct") != "guide" || query.Get("ac") != "status" {
		return false
	}
	for key := range query {
		if key != "ct" && key != "ac" && key != "_" {
			return false
		}
	}
	return true
}

func (t *reuseOnlyTransport) audit(entry readAudit) {
	if t.auditPath == "" {
		return
	}
	f, err := os.OpenFile(t.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		defer f.Close()
		_ = json.NewEncoder(f).Encode(entry)
	}
}

func (t *reuseOnlyTransport) counts() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests, t.blocked
}

type sampleScope struct {
	Roots   map[string]string `json:"roots"`
	Samples []struct {
		PickCode string `json:"pickcode"`
		Matches  []struct {
			FileID   string `json:"file_id"`
			PickCode string `json:"pickcode"`
			ScopeID  string `json:"scope_id"`
			Verified bool   `json:"ancestry_verified"`
			Ancestry []struct {
				FileID json.RawMessage `json:"file_id"`
			} `json:"ancestry"`
		} `json:"matches"`
	} `json:"samples"`
}

func verifiedPicks(raw []byte) ([]string, error) {
	var scope sampleScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil, errors.New("invalid scope manifest")
	}
	numeric := regexp.MustCompile(`^[1-9][0-9]*$`)
	for _, name := range []string{"转存文件夹", "媒体库"} {
		if !numeric.MatchString(scope.Roots[name]) {
			return nil, fmt.Errorf("missing allowed directory %s", name)
		}
	}
	if len(scope.Samples) == 0 || len(scope.Samples) > 4 {
		return nil, errors.New("one to four verified samples required")
	}
	picks := []string{}
	seen := map[string]bool{}
	for _, sample := range scope.Samples {
		if len(sample.Matches) != 1 {
			return nil, errors.New("sample file not uniquely matched")
		}
		match := sample.Matches[0]
		if !numeric.MatchString(match.FileID) || !match.Verified || match.ScopeID != scope.Roots["媒体库"] || match.PickCode != sample.PickCode {
			return nil, errors.New("sample outside verified library scope")
		}
		inScope := false
		for _, parent := range match.Ancestry {
			if strings.Trim(string(parent.FileID), "\"") == match.ScopeID {
				inScope = true
			}
		}
		if !inScope || seen[sample.PickCode] {
			return nil, errors.New("sample ancestry missing or pickcode duplicated")
		}
		seen[sample.PickCode] = true
		picks = append(picks, sample.PickCode)
	}
	return picks, nil
}

func independentPan(ctx context.Context, data string, vault *secure.Vault) (*pan115.DriverProvider, *reuseOnlyTransport, []string, error) {
	raw, err := os.ReadFile(filepath.Join(data, "scope.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	picks, err := verifiedPicks(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	sealed, err := os.ReadFile(filepath.Join(data, "pan-cookie.enc"))
	if err != nil {
		return nil, nil, nil, err
	}
	cookie, err := vault.Open(strings.TrimSpace(string(sealed)))
	if err != nil {
		return nil, nil, nil, errors.New("Cookie decryption failed")
	}
	guard := &reuseOnlyTransport{base: http.DefaultTransport.(*http.Transport).Clone(), maximum: 20, interval: 5 * time.Second, auditPath: filepath.Join(data, "pan-read-audit.jsonl")}
	client := &http.Client{Transport: guard, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	pan := pan115.New()
	pan.ConfigureHTTP(client)
	if err = pan.SetCookie(cookie); err != nil {
		return nil, nil, nil, errors.New("invalid imported Cookie")
	}
	if err = pan.Check(ctx); err != nil {
		return nil, nil, nil, errors.New("existing Cookie check failed; test stopped without login")
	}
	return pan, guard, picks, nil
}
