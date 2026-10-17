package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/store"
)

func TestSignedBackendProbeThroughPublishedAddress(t *testing.T) {
	var discoveries atomic.Int64
	var apiKey atomic.Value
	apiKey.Store("backend-api-key")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := apiKey.Load().(string)
		if r.URL.Path != "/System/Info" || r.Header.Get("X-Emby-Token") != key || !strings.Contains(r.Header.Get("Authorization"), `Token="`+key+`"`) {
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		discoveries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"LocalAddress": "http://172.30.0.2:8096", "LocalAddresses": []string{"http://[fd00::2]:8096"}})
	}))
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	pan := &fakePan{cdn: "https://cdn.example"}
	// The configured loopback address represents a host-published endpoint,
	// while the backend's request originates from its private container address.
	gateway := NewGateway(st, pan, secret, func(context.Context) (Config, error) {
		return Config{URL: backend.URL, APIKey: apiKey.Load().(string)}, nil
	})
	path := "/direct/media?sig=" + organize.SignMedia(secret, "media") + "&jellyfin_sig=" + organize.SignJellyfinRequest(secret, "media")
	for _, remote := range []string{"172.30.0.2:45678", "[fd00::2]:45678", "[::ffff:172.30.0.2]:45678"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, path, nil)
			req.RemoteAddr = remote
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)
			if w.Code != http.StatusFound {
				t.Fatalf("%s %s: %d %s", method, remote, w.Code, w.Body.String())
			}
		}
	}
	if discoveries.Load() != 1 {
		t.Fatalf("repeated probes did not share cached backend addresses: %d", discoveries.Load())
	}
	for _, test := range []struct {
		name, query, remote, stage string
		status                     int
	}{
		{"unrelated private source", path, "172.30.0.20:45678", "backend_origin_mismatch", http.StatusForbidden},
		{"unrelated public source", path, "192.0.2.20:45678", "backend_origin_mismatch", http.StatusForbidden},
		{"missing backend signature", "/direct/media?sig=" + organize.SignMedia(secret, "media"), "172.30.0.2:45678", "login_required", http.StatusUnauthorized},
		{"invalid backend signature", "/direct/media?sig=" + organize.SignMedia(secret, "media") + "&jellyfin_sig=invalid", "172.30.0.2:45678", "backend_signature_invalid", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.query, nil)
			req.RemoteAddr = test.remote
			req.Header.Set("X-Forwarded-For", "172.30.0.2")
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)
			if w.Code != test.status || w.Header().Get("X-115-Direct-Error") != test.stage {
				t.Fatalf("status=%d stage=%s body=%s", w.Code, w.Header().Get("X-115-Direct-Error"), w.Body.String())
			}
		})
	}
	apiKey.Store("replacement-api-key")
	req := httptest.NewRequest(http.MethodHead, path, nil)
	req.RemoteAddr = "172.30.0.2:45678"
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	if w.Code != http.StatusFound || discoveries.Load() != 2 {
		t.Fatalf("changed server credentials did not invalidate discovery: status=%d discoveries=%d", w.Code, discoveries.Load())
	}
}

func TestDirectAuthorizationDistinguishesLoginPermissionAndBackendFailure(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Emby-Token") {
		case "allowed", "blocked":
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": r.Header.Get("X-Emby-Token") == "allowed"}})
		case "server-error":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			http.Error(w, "denied", http.StatusUnauthorized)
		}
	}))
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	gateway := NewGateway(st, &fakePan{cdn: "https://cdn.example"}, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, test := range []struct {
		token, stage string
		status       int
	}{
		{"", "login_required", http.StatusUnauthorized},
		{"expired", "token_rejected", http.StatusUnauthorized},
		{"blocked", "playback_forbidden", http.StatusForbidden},
		{"server-error", "user_authorization_unavailable", http.StatusBadGateway},
		{"allowed", "", http.StatusFound},
	} {
		t.Run(test.stage+test.token, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/direct/media?sig="+organize.SignMedia(secret, "media"), nil)
			req.Header.Set("X-Emby-Token", test.token)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)
			if w.Code != test.status || w.Header().Get("X-115-Direct-Error") != test.stage {
				t.Fatalf("status=%d stage=%s body=%s", w.Code, w.Header().Get("X-115-Direct-Error"), w.Body.String())
			}
			events := gateway.PlaybackEvents("media")
			if test.stage != "" && (len(events) == 0 || events[0].Stage != test.stage) {
				t.Fatalf("authorization cause missing from playback events: %+v", events)
			}
		})
	}
}

func TestBackendDiscoveryFailureAndRedirect(t *testing.T) {
	var redirected atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer other.Close()
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"rejected API key", http.StatusUnauthorized, "denied"},
		{"unavailable server", http.StatusServiceUnavailable, "unavailable"},
		{"invalid response", http.StatusOK, "not json"},
		{"redirect", http.StatusFound, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", other.URL)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer backend.Close()
			gateway := NewGateway(gatewayStore(t), nil, nil, func(context.Context) (Config, error) { return Config{URL: backend.URL, APIKey: "backend-api-key"}, nil })
			if err := gateway.authorizeBackendOrigin(context.Background(), "172.30.0.2:45678"); err != errBackendDiscovery {
				t.Fatalf("expected backend discovery failure, got %v", err)
			}
			if !gateway.backendOrigin.expires.Before(time.Now().Add(10 * time.Second)) {
				t.Fatal("failed discovery was cached too long")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("backend discovery followed redirect with API credentials")
	}
}
