package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientUsesCurrentAndLegacyAuthenticationHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "api-key" || !strings.Contains(r.Header.Get("Authorization"), `Token="api-key"`) {
			http.Error(w, "missing credentials", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/System/Info":
			_ = json.NewEncoder(w).Encode(map[string]any{"ServerName": "test", "Version": "12.1.0"})
		case "/Library/Refresh":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	client := NewClient(func(context.Context) (Config, error) {
		return Config{URL: backend.URL, APIKey: "api-key"}, nil
	})
	info, err := client.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info["Version"] != "12.1.0" {
		t.Fatalf("unexpected server info: %#v", info)
	}
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientExplainsRejectedAPIKey(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer backend.Close()
	client := NewClient(func(context.Context) (Config, error) {
		return Config{URL: backend.URL, APIKey: "rejected"}, nil
	})
	_, err := client.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API Key 被拒绝") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJellyfinTokenReadsModernAuthorizationHeaders(t *testing.T) {
	tests := map[string]string{
		`MediaBrowser Client="App", Device="Phone", Token="viewer"`: "viewer",
		`Emby Token="viewer"`: "viewer",
		`Bearer viewer`:       "viewer",
	}
	for header, expected := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", header)
		if actual := jellyfinToken(req); actual != expected {
			t.Errorf("header %q: got %q, want %q", header, actual, expected)
		}
	}
	for _, test := range []struct{ path, key, value string }{
		{"/?API_KEY=viewer", "", ""},
		{"/?x-emby-token=viewer", "", ""},
		{"/", "X-MediaBrowser-Token", "viewer"},
		{"/", "X-Emby-Authorization", `MediaBrowser Client="App", Token="viewer"`},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		if test.key != "" {
			req.Header.Set(test.key, test.value)
		}
		if actual := jellyfinToken(req); actual != "viewer" {
			t.Errorf("token variant %s %s: %q", test.path, test.key, actual)
		}
	}
}
