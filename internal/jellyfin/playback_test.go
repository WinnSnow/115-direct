package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/store"
)

type sequencePan struct {
	fakePan
	urls      []string
	active    atomic.Int64
	maxActive atomic.Int64
}

type trackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedBody) Close() error { b.closed.Store(true); return nil }

func (p *sequencePan) DownloadURL(ctx context.Context, pc, ua string) (string, map[string]string, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.maxActive.Load(); n > old; old = p.maxActive.Load() {
		if p.maxActive.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case <-time.After(5 * time.Millisecond):
	}
	i := int(p.calls.Add(1)) - 1
	if i >= len(p.urls) {
		i = len(p.urls) - 1
	}
	return fmt.Sprintf("%s?f=1&t=%d", p.urls[i], time.Now().Add(time.Hour).Unix()), map[string]string{"User-Agent": ua}, nil
}

func TestLinkCheckRetriesStaleAndCachesSuccessfulProbe(t *testing.T) {
	var probes atomic.Int64
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.Header.Get("Range") != "bytes=0-0" || r.UserAgent() != "Player/1" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("invalid probe headers")
		}
		if r.URL.Path == "/stale" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.WriteHeader(206)
		_, _ = w.Write([]byte{0})
	}))
	defer cdn.Close()
	p := &sequencePan{urls: []string{cdn.URL + "/stale", cdn.URL + "/fresh"}}
	g := NewGateway(gatewayStore(t), p, nil, nil)
	r, err := g.resolveLink(context.Background(), "pc", "Player/1", true)
	if err != nil || r.Attempts != 2 || r.ProbeStatus != 206 || r.Cache != "refreshed" {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	r, err = g.resolveLink(context.Background(), "pc", "Player/1", true)
	if err != nil || r.Cache != "hit" || r.ProbeStatus != 206 || p.calls.Load() != 2 || probes.Load() != 2 {
		t.Fatalf("cache result=%+v error=%v calls=%d probes=%d", r, err, p.calls.Load(), probes.Load())
	}
}

func TestLinkChecksNeverFollowRedirectAndStopAfterOneRetry(t *testing.T) {
	var followed atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer destination.Close()
	for _, status := range []int{302, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
			}))
			defer cdn.Close()
			p := &sequencePan{urls: []string{cdn.URL}}
			g := NewGateway(gatewayStore(t), p, nil, nil)
			_, err := g.resolveLink(context.Background(), "pc", "Player/1", true)
			expected := int64(1)
			if status == 403 {
				expected = 2
			}
			if err == nil || p.calls.Load() != expected || followed.Load() != 0 {
				t.Fatalf("err=%v calls=%d followed=%d", err, p.calls.Load(), followed.Load())
			}
			g.mu.Lock()
			entries := len(g.cache)
			g.mu.Unlock()
			if entries != 0 {
				t.Fatal("failed link retained in cache")
			}
		})
	}
}

func TestDirectLinkAcquisitionIsSerializedAndCoalesced(t *testing.T) {
	p := &sequencePan{urls: []string{"https://cdn.example/file"}}
	g := NewGateway(gatewayStore(t), p, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := g.resolveLink(context.Background(), fmt.Sprint(i%2), "Player/1", false); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if p.maxActive.Load() != 1 || p.calls.Load() != 2 {
		t.Fatalf("active=%d calls=%d", p.maxActive.Load(), p.calls.Load())
	}
}

func TestOriginalStreamEntrypointsAndClientRange(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "viewer" {
			http.Error(w, "denied", 401)
			return
		}
		switch r.URL.Path {
		case "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
		case "/Items/item/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": "https://gateway/direct/media?sig=" + organize.SignMedia(secret, "media")}}})
		default:
			t.Error("media bytes reached Jellyfin")
			w.WriteHeader(500)
		}
	}))
	defer backend.Close()
	var cdnReads atomic.Int64
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnReads.Add(1)
		if r.Header.Get("Range") != "bytes=100-103" || !strings.Contains(r.UserAgent(), "VLC") {
			t.Error("range/user agent not retained by client redirect")
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 100-103/1024")
		w.WriteHeader(206)
		_, _ = w.Write([]byte("data"))
	}))
	defer cdn.Close()
	st := gatewayStore(t)
	_ = st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mp4"})
	p := &fakePan{cdn: cdn.URL}
	g := NewGateway(st, p, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, entry := range []string{"/Videos/item/stream", "/videos/item/stream.mp4", "/Audio/item/stream.flac", "/Audio/item/universal", "/Items/item/Download", "/Items/item/File", "/emby/Videos/item/stream.mkv", "/jellyfin/Items/item/Download", "/direct/media/file.mp4?sig=" + organize.SignMedia(secret, "media")} {
		separator := "?"
		if strings.Contains(entry, "?") {
			separator = "&"
		}
		r := httptest.NewRequest(http.MethodHead, entry+separator+"API_KEY=viewer&mediaSourceId=source", nil)
		r.Header.Set("User-Agent", "VLC/3.0")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 302 || w.Body.Len() != 0 {
			t.Fatalf("%s status=%d body=%s", entry, w.Code, w.Body.String())
		}
	}
	if p.calls.Load() != 1 || cdnReads.Load() != 0 {
		t.Fatalf("gateway downloaded content or missed cache: pan=%d cdn=%d", p.calls.Load(), cdnReads.Load())
	}
	server := httptest.NewServer(g)
	defer server.Close()
	for _, path := range []string{"/Videos/item/stream.mp4?api_key=viewer", "/direct/media/file.mp4?sig=" + organize.SignMedia(secret, "media") + "&api_key=viewer"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("User-Agent", "VLC/3.0")
		req.Header.Set("Range", "bytes=100-103")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 206 || string(body) != "data" {
			t.Fatalf("range response=%d %s", resp.StatusCode, body)
		}
	}
}

func TestPlaybackReportsPreserveRequestAndDoNotProveCDNPlayback(t *testing.T) {
	const body = `{"ItemId":"item","MediaSourceId":"source","PositionTicks":100000000,"PlayMethod":"DirectPlay"}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != body {
			t.Errorf("request modified: %s", raw)
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	g := NewGateway(gatewayStore(t), &fakePan{}, nil, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	g.sources["item|source"] = "media"
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress?api_key=viewer", strings.NewReader(body))
	tracked := &trackedBody{Reader: strings.NewReader(body)}
	req.Body = tracked
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	events := g.PlaybackEvents("media")
	if w.Code != 204 || len(events) != 1 || events[0].Mode != "client_progress" || events[0].PositionTicks != 100000000 || events[0].Stage != "client_report" {
		t.Fatalf("status=%d events=%+v", w.Code, events)
	}
	if !tracked.closed.Load() {
		t.Fatal("original request body not closed")
	}
}

func TestPublicPlaybackURLsAndOriginalContainer(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	st := gatewayStore(t)
	_ = st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", Name: "movie.MKV", PickCode: "pc"})
	_ = st.PutSetting(context.Background(), "playback", PlaybackOptions{PublicURL: "https://media.example.com"})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "old-playback")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": "http://internal/direct/media?sig=" + organize.SignMedia(secret, "media") + "&jellyfin_sig=backend", "Container": "mov,mp4", "SupportsTranscoding": true, "TranscodingUrl": "/hls", "RequiresOpening": true, "OpenToken": "backend"}}})
	}))
	defer backend.Close()
	g := NewGateway(st, &fakePan{}, secret, func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://replaced-tunnel-host/Items/item/PlaybackInfo?api_key=viewer", strings.NewReader(`{}`))
		g.ServeHTTP(w, r)
		var payload struct{ MediaSources []map[string]any }
		if json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.MediaSources) != 1 {
			t.Fatalf("unexpected playback response: %d", w.Code)
		}
		s := payload.MediaSources[0]
		if s["Container"] != "mkv" || !strings.HasPrefix(s["Path"].(string), "https://media.example.com/direct/media/file.mkv?") || !strings.HasPrefix(s["DirectStreamUrl"].(string), "/direct/media/file.mkv?") || s["RequiresOpening"] != false || s["SupportsTranscoding"] != false || strings.Contains(s["Path"].(string), "jellyfin_sig") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
			t.Fatalf("invalid source: %+v headers=%+v", s, w.Header())
		}
	}
	d := g.Diagnose(context.Background(), "media", "VLC/3", false)
	// Missing CDN URL is an explicit diagnostic failure, not playback success.
	if d.RedirectReady || d.ClientPlaybackVerified || d.Stage != "link_resolve" {
		t.Fatalf("invalid diagnostic: %+v", d)
	}
}
