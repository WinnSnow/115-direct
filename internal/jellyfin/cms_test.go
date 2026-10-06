package jellyfin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCMSOriginsAndExtensionlessSources(t *testing.T) {
	b, err := NewCMSBridge("http://cms.test:9527", []string{"http://cms.example.test:9527"}, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://cms.example.test:9527/d/fixturepick123.mkv?/中文.mkv", "http://cms.example.test:9527/d/fixturepick123?/中文.mkv"} {
		source, ok := b.ParseSource(raw)
		if !ok || source.PickCode != "fixturepick123" {
			t.Fatal("valid source rejected")
		}
	}
	for _, raw := range []string{"http://evil.test/d/fixturepick123.mkv", "http://cms.example.test:9527/d/fixturepick123.html", "http://user:pass@192.0.2.7:9527/d/fixturepick123", "http://cms.example.test:9527/d/fixturepick123/other"} {
		if _, ok := b.ParseSource(raw); ok {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestIndependentBridgeNoCMSFallbackAndScopedLegacyListener(t *testing.T) {
	calls := 0
	b, err := NewIndependentCMSBridge([]string{"http://cms.example.test:9527"}, []string{"fixturepick123"}, func(ctx context.Context, pick, ua string) (string, map[string]string, error) {
		calls++
		if pick != "fixturepick123" {
			t.Fatal("unapproved pickcode reached resolver")
		}
		return "https://cdn.test/video.mkv", map[string]string{"User-Agent": ua}, nil
	}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b.upstream != nil || b.client != nil || b.Mode() != "independent-115" {
		t.Fatal("independent mode retains CMS transport")
	}
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		b.ServeLegacyHTTP(w, httptest.NewRequest(method, "/d/fixturepick123?/中文.mkv", nil))
		if w.Code != 302 || w.Header().Get("X-CMS-Link-Mode") != "independent-115" {
			t.Fatalf("legacy redirect failed: %d", w.Code)
		}
	}
	if calls != 1 {
		t.Fatal("legacy cache not used")
	}
	for _, path := range []string{"/d/otherpick123.mkv", "/d/fixturepick123.html", "/files/delete"} {
		w := httptest.NewRecorder()
		b.ServeLegacyHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unapproved legacy path accepted")
		}
	}
	if _, _, err := b.lookup(context.Background(), "http://cms.example.test:9527/d/otherpick123", "Player"); err == nil {
		t.Fatal("unapproved ticket source reached resolver")
	}
	if calls != 1 {
		t.Fatal("unapproved request reached 115")
	}
	b.independent = func(context.Context, string, string) (string, map[string]string, error) {
		calls++
		return "", nil, io.ErrUnexpectedEOF
	}
	if _, _, err := b.lookup(context.Background(), "http://cms.example.test:9527/d/fixturepick123", "Player"); err == nil {
		t.Fatal("resolver failure hidden")
	}
	if _, _, err := b.lookup(context.Background(), "http://cms.example.test:9527/d/fixturepick123", "Player"); err == nil {
		t.Fatal("request budget ignored")
	}
	if calls != 2 {
		t.Fatal("failed lookup retried through another upstream")
	}
}

func TestIndependentTicketRedirectUses115Resolver(t *testing.T) {
	b, err := NewIndependentCMSBridge([]string{"http://cms.example.test:9527"}, []string{"fixturepick123"}, func(context.Context, string, string) (string, map[string]string, error) {
		return "https://cdn.test/video.mkv", nil, nil
	}, 2, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
	}))
	defer backend.Close()
	g := NewGateway(gatewayStore(t), nil, []byte("secret"), func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	g.CMS = b
	source, _ := b.ParseSource("http://cms.example.test:9527/d/fixturepick123.mkv?/中文.mkv")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", b.playPath(g.Secret, source, "mkv", "viewer"), nil))
	if w.Code != 302 || w.Header().Get("X-CMS-Link-Mode") != "independent-115" {
		t.Fatalf("independent ticket failed: %d", w.Code)
	}
}

func TestCMSRewriteScopedTicketAndCache(t *testing.T) {
	var calls atomic.Int64
	cms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/d/fixturepick123" || r.UserAgent() != "Player/1" || r.Header.Get("X-Emby-Token") != "" || r.Header.Get("Authorization") != "" {
			t.Error("upstream path, UA or credentials incorrect")
		}
		if !strings.Contains(r.URL.RawQuery, "%E4%B8%AD") {
			t.Error("Chinese query was not HTTP encoded")
		}
		w.Header().Set("Location", "https://cdn.test/video.mkv")
		w.WriteHeader(302)
	}))
	defer cms.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" && r.Header.Get("X-Emby-Token") != "blocked" {
			json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]bool{"EnableMediaPlayback": true}})
			return
		}
		http.Error(w, "denied", 401)
	}))
	defer backend.Close()
	b, err := NewCMSBridge(cms.URL, []string{"http://cms.example.test:9527"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGateway(gatewayStore(t), nil, []byte("test-secret"), func(context.Context) (Config, error) { return Config{URL: backend.URL}, nil })
	g.CMS = b
	r := httptest.NewRequest("POST", "http://gateway/Items/item/PlaybackInfo?api_key=viewer", nil)
	body := `{"MediaSources":[{"Id":"source","Container":"mkv","Path":"http://cms.example.test:9527/d/fixturepick123?/中文.mkv","TranscodingUrl":"/hls","SupportsTranscoding":true}]}`
	resp := jsonResponseForCMS(body, r)
	if err := g.rewritePlaybackInfoFor(resp, r); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		MediaSources []struct {
			Path                string
			DirectStreamUrl     string
			SupportsTranscoding bool
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	source := payload.MediaSources[0]
	if !strings.HasPrefix(source.DirectStreamUrl, "/cms/play/") || source.SupportsTranscoding {
		t.Fatal("source not rewritten")
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", source.DirectStreamUrl, nil)
		req.Header.Set("User-Agent", "Player/1")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		if w.Code != 302 || w.Header().Get("Location") != "https://cdn.test/video.mkv" {
			t.Fatalf("redirect: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("cache repeated upstream request")
	}
	for _, token := range []string{"other-viewer", "blocked"} {
		u, _ := url.Parse(source.DirectStreamUrl)
		q := u.Query()
		q.Set("api_key", token)
		u.RawQuery = q.Encode()
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("GET", u.String(), nil))
		if w.Code != 403 && w.Code != 401 {
			t.Fatal("ticket usable by another user")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("rejected request reached CMS")
	}
}

func TestCMSBudgetAndNonRedirectNeverProxyVideo(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	b, err := NewCMSBridge(upstream.URL, []string{"http://old.test:9527"}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := b.lookup(context.Background(), "http://old.test:9527/d/fixturepick123.mkv", "Player/1"); err == nil {
			t.Fatal("non-302 response accepted")
		}
	}
	if calls.Load() != 1 || b.Requests() != 1 {
		t.Fatal("request budget not enforced")
	}
}

func jsonResponseForCMS(body string, r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
