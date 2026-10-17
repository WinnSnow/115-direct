package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/store"
)

func TestPlaybackTimingMeasuresTheSameColdWarmAndRejectedRequest(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	const backendDelay = 15 * time.Millisecond
	const downloadDelay = 20 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "viewer-secret" {
			http.Error(w, "denied", 401)
			return
		}
		time.Sleep(backendDelay)
		_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
	}))
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mp4"}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	g := NewGateway(st, nil, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	g.linkLookup = func(context.Context, string, string) (string, map[string]string, error) {
		time.Sleep(downloadDelay)
		return "https://cdn.example/video?private-cdn-signature=hidden", nil, nil
	}
	for _, test := range []struct {
		name, token string
		status      int
	}{
		{"cold", "viewer-secret", 302},
		{"warm", "viewer-secret", 302},
		{"rejected", "", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			path := "/direct/media/file.mp4?sig=" + organize.SignMedia(secret, "media") + "&api_key=" + test.token
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d", w.Code)
			}
			record := decodeGatewayTiming(t, logs.Bytes())
			if record["request_id"] != w.Header().Get("X-115-Direct-Request-ID") || record["request_id"] == "" || record["path"] != "/direct/media/file.mp4" {
				t.Fatalf("request correlation missing: %+v", record)
			}
			elapsed, ready := record["elapsed_ms"].(float64), record["headers_ready_ms"].(float64)
			if elapsed < ready || ready <= 0 || record["status"].(float64) != float64(test.status) {
				t.Fatalf("invalid timing boundaries: %+v", record)
			}
			started, err := time.Parse(time.RFC3339Nano, record["request_started"].(string))
			if err != nil {
				t.Fatal(err)
			}
			finished, err := time.Parse(time.RFC3339Nano, record["request_finished"].(string))
			if err != nil || !finished.After(started) || !strings.Contains(w.Header().Get("Server-Timing"), "gateway;dur=") {
				t.Fatalf("missing response timing: %+v", record)
			}
			if test.name == "cold" {
				if record["authorization_ms"].(float64) < milliseconds(backendDelay) || record["backend_headers_ms"].(float64) < milliseconds(backendDelay) || record["link_job_115_ms"].(float64) < milliseconds(downloadDelay) || record["link_resolve_ms"].(float64) < milliseconds(downloadDelay) || record["backend_calls"].(float64) != 1 {
					t.Fatalf("cold request waits not measured: %+v", record)
				}
				if elapsed < record["authorization_ms"].(float64)+record["link_resolve_ms"].(float64) {
					t.Fatalf("phases exceed request duration: %+v", record)
				}
			} else if record["backend_calls"].(float64) != 0 || record["link_job_115_ms"].(float64) != 0 || record["link_job_queue_ms"].(float64) != 0 {
				t.Fatalf("cached or rejected request inherited another request's work: %+v", record)
			}
			if strings.Contains(logs.String(), "viewer-secret") || strings.Contains(logs.String(), "private-cdn-signature") || strings.Contains(logs.String(), "?sig=") {
				t.Fatal("timing log exposed a token or signed URL")
			}
		})
	}
}

func TestPlaybackInfoTimingIncludesBackendWaitAndResponseRewrite(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	const backendDelay = 20 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(backendDelay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"MediaSources":[]}`)
	}))
	defer backend.Close()
	g := NewGateway(gatewayStore(t), nil, nil, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	r := httptest.NewRequest(http.MethodPost, "/Items/item/PlaybackInfo?api_key=hidden", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	record := decodeGatewayTiming(t, logs.Bytes())
	if w.Code != 200 || record["backend_calls"].(float64) != 1 || record["backend_headers_ms"].(float64) < milliseconds(backendDelay) || record["playback_info_rewrite_ms"].(float64) <= 0 || record["elapsed_ms"].(float64) < milliseconds(backendDelay) {
		t.Fatalf("backend wait missing from request timing: %+v", record)
	}
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("timing wrapper broke JSON response")
	}
}

func decodeGatewayTiming(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) == nil && record["msg"] == "gateway request" {
			if result != nil {
				t.Fatal("more than one request timing record")
			}
			result = record
		}
	}
	if result == nil {
		t.Fatal("request timing was not recorded")
	}
	return result
}
