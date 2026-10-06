package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

type fakePan struct {
	calls  atomic.Int64
	lastUA string
	cdn    string
}

func (f *fakePan) SetCookie(string) error                                   { return nil }
func (f *fakePan) Check(context.Context) error                              { return nil }
func (f *fakePan) StartQR(context.Context) (*pan115.QRSession, error)       { return nil, nil }
func (f *fakePan) PollQR(context.Context, string) (*pan115.QRStatus, error) { return nil, nil }
func (f *fakePan) List(context.Context, string) ([]pan115.Entry, error)     { return nil, nil }
func (f *fakePan) Mkdir(context.Context, string, string) (string, error)    { return "", nil }
func (f *fakePan) SnapshotShare(context.Context, string, string) (*pan115.ShareSnapshot, error) {
	return nil, nil
}
func (f *fakePan) ReceiveShare(context.Context, *pan115.ShareSnapshot, string, string) error {
	return nil
}
func (f *fakePan) Copy(context.Context, string, ...string) error { return nil }
func (f *fakePan) Move(context.Context, string, ...string) error { return nil }
func (f *fakePan) Rename(context.Context, string, string) error  { return nil }
func (f *fakePan) Delete(context.Context, ...string) error       { return nil }
func (f *fakePan) DownloadURL(_ context.Context, _ string, ua string) (string, map[string]string, error) {
	f.calls.Add(1)
	f.lastUA = ua
	return fmt.Sprintf("%s/video?f=1&t=%d", f.cdn, time.Now().Add(time.Hour).Unix()), map[string]string{"User-Agent": ua}, nil
}

func gatewayStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	vault, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDirectRequiresJellyfinPermissionAndCachesByUA(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" && r.Header.Get("X-Emby-Token") == "viewer" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
			return
		}
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer backend.Close()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("gateway followed CDN redirect") }))
	defer cdn.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	pan := &fakePan{cdn: cdn.URL}
	secret := []byte("01234567890123456789012345678901")
	gateway := NewGateway(st, pan, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	path := "/direct/media?sig=" + organize.SignMedia(secret, "media") + "&api_key=viewer"
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("User-Agent", "MobilePlayer/1")
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, req)
		if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), cdn.URL) {
			t.Fatalf("redirect %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if pan.calls.Load() != 1 || pan.lastUA != "MobilePlayer/1" {
		t.Fatalf("cache/UA mismatch: calls=%d ua=%q", pan.calls.Load(), pan.lastUA)
	}
	req := httptest.NewRequest(http.MethodGet, "/direct/media?sig="+organize.SignMedia(secret, "media")+"&api_key=blocked", nil)
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("blocked status: %d", w.Code)
	}
}

func TestPlaybackInfoIsRewritten(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	sig := organize.SignMedia(secret, "media")
	jellyfinSig := organize.SignJellyfinRequest(secret, "media")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("PlaybackInfo compression not disabled: %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{
			"Path": "http://gateway/direct/media?sig=" + sig + "&jellyfin_sig=" + jellyfinSig, "SupportsTranscoding": true, "TranscodingUrl": "/videos/media/master.m3u8",
			"TranscodingContainer": "ts", "TranscodingSubProtocol": "hls",
		}, map[string]any{"Path": "/media/local.mp4", "SupportsTranscoding": true, "TranscodingUrl": "/videos/local/master.m3u8"}}})
	}))
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(st, &fakePan{}, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	req := httptest.NewRequest(http.MethodGet, "/Items/1/PlaybackInfo", nil)
	req.Header.Set("X-Emby-Token", "viewer")
	req.Header.Set("Accept-Encoding", "gzip, br")
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	var payload struct{ MediaSources []map[string]any }
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	source := payload.MediaSources[0]
	if source["SupportsTranscoding"] != false || source["SupportsDirectPlay"] != true || source["SupportsDirectStream"] != true || !strings.Contains(source["DirectStreamUrl"].(string), "api_key=viewer") || strings.Contains(source["Path"].(string), "jellyfin_sig") {
		t.Fatalf("not rewritten: %#v", source)
	}
	for _, key := range []string{"TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol"} {
		if _, exists := source[key]; exists {
			t.Fatalf("transcoding field retained: %s", key)
		}
	}
	local := payload.MediaSources[1]
	if local["SupportsTranscoding"] != true || local["TranscodingUrl"] != "/videos/local/master.m3u8" {
		t.Fatalf("non-project source changed: %#v", local)
	}
}

func TestSegmentedPlaybackNeverReachesJellyfinForDirectMedia(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	var playbackRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			path := "https://gateway/direct/media?sig=" + organize.SignMedia(secret, "media")
			if strings.Contains(r.URL.Path, "/local/") {
				path = "/media/local.mp4"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": path}}})
		case r.URL.Path == "/Users/Me":
			if r.Header.Get("X-Emby-Token") != "viewer" {
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
		default:
			playbackRequests.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer backend.Close()
	pan := &fakePan{}
	gateway := NewGateway(gatewayStore(t), pan, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, path := range []string{
		"/Videos/item/master.m3u8", "/videos/item/main.m3u8", "/Videos/item/stream.m3u8",
		"/Videos/item/hls1/main/47.mp4", "/Videos/item/hls/main/0.ts", "/Videos/item/dash/main/0.m4s", "/Videos/item/manifest.mpd", "/Videos/item/stream.mpd",
	} {
		t.Run(path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				w := httptest.NewRecorder()
				gateway.ServeHTTP(w, httptest.NewRequest(method, path+"?api_key=viewer&MediaSourceId=source", nil))
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "direct_play_required") {
					t.Fatalf("unexpected segmented response: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
	if playbackRequests.Load() != 0 || pan.calls.Load() != 0 {
		t.Fatalf("blocked playback reached upstream: Jellyfin=%d 115=%d", playbackRequests.Load(), pan.calls.Load())
	}
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?api_key=blocked", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized response: %d", w.Code)
	}
	w = httptest.NewRecorder()
	gateway.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/Videos/local/master.m3u8?api_key=viewer", nil))
	if w.Code != http.StatusOK || playbackRequests.Load() != 1 {
		t.Fatalf("non-project playback not preserved: %d, upstream calls=%d", w.Code, playbackRequests.Load())
	}
}

func TestMediaLookupFailureDoesNotFallBackToPlaybackProxy(t *testing.T) {
	var playbackRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		playbackRequests.Add(1)
	}))
	defer backend.Close()
	gateway := NewGateway(gatewayStore(t), &fakePan{}, nil, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, path := range []string{"/Videos/item/stream.mp4", "/Videos/item/master.m3u8"} {
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?api_key=viewer", nil))
		if w.Code != http.StatusBadGateway || playbackRequests.Load() != 0 {
			t.Fatalf("failed lookup fell through: %d, upstream calls=%d", w.Code, playbackRequests.Load())
		}
	}
}

func TestDirectMediaSourceSelectionFailsClosed(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	var playbackRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
			sig := organize.SignMedia(secret, "media")
			if strings.Contains(r.URL.Path, "/invalid/") {
				sig = "invalid"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{
				map[string]any{"Id": "direct", "Path": "https://gateway/direct/media?sig=" + sig},
				map[string]any{"Id": "local", "Path": "/media/local.mp4"},
			}})
			return
		}
		playbackRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	pan := &fakePan{}
	gateway := NewGateway(gatewayStore(t), pan, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, test := range []struct {
		path string
		code int
	}{
		{"/Videos/item/master.m3u8?MediaSourceId=unknown", http.StatusBadRequest},
		{"/Videos/invalid/master.m3u8?MediaSourceId=direct", http.StatusForbidden},
		{"/Videos/invalid/stream.mp4?MediaSourceId=direct", http.StatusForbidden},
		{"/Videos/item/master.m3u8?MediaSourceId=local", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, httptest.NewRequest(http.MethodGet, test.path+"&api_key=viewer", nil))
		if w.Code != test.code {
			t.Fatalf("%s: got %d, want %d", test.path, w.Code, test.code)
		}
	}
	if playbackRequests.Load() != 1 || pan.calls.Load() != 0 {
		t.Fatalf("unexpected upstream calls: Jellyfin=%d 115=%d", playbackRequests.Load(), pan.calls.Load())
	}
}

func TestConfiguredJellyfinBackendCanProbeSignedSTRM(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789012345678901")
	pan := &fakePan{cdn: "https://cdn.example"}
	gateway := NewGateway(st, pan, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	path := "/direct/media?sig=" + organize.SignMedia(secret, "media") + "&jellyfin_sig=" + organize.SignJellyfinRequest(secret, "media")

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:45678"
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("Jellyfin backend status: %d %s", w.Code, w.Body.String())
	}

	for name, mutate := range map[string]func(*http.Request){
		"wrong source": func(req *http.Request) { req.RemoteAddr = "192.0.2.20:45678" },
		"missing signature": func(req *http.Request) {
			req.RemoteAddr = "127.0.0.1:45678"
			req.URL.RawQuery = "sig=" + organize.SignMedia(secret, "media")
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			mutate(req)
			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status: %d", w.Code)
			}
		})
	}
}

func TestCanonicalVideoStreamRedirects(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	sig := organize.SignMedia(secret, "media")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Items/item/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": "https://gateway/direct/media?sig=" + sig}}})
		case "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	st := gatewayStore(t)
	if err := st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv"}); err != nil {
		t.Fatal(err)
	}
	pan := &fakePan{cdn: "https://cdn.example"}
	gateway := NewGateway(st, pan, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	req := httptest.NewRequest(http.MethodGet, "/Videos/item/stream.mkv?MediaSourceId=source&api_key=viewer", nil)
	req.Header.Set("User-Agent", "Mobile/1")
	w := httptest.NewRecorder()
	gateway.ServeHTTP(w, req)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "https://cdn.example") {
		t.Fatalf("canonical stream not redirected: %d %s", w.Code, w.Body.String())
	}
}
