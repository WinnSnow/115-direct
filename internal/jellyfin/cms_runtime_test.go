package jellyfin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cmsTransportFunc func(*http.Request) (*http.Response, error)

func (f cmsTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCMSReuseTransportBlocksLoginWritesAndUnexpectedQueries(t *testing.T) {
	calls := 0
	tpt := CMSReadTransport{Base: cmsTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	})}
	for _, url := range []string{"https://passportapi.115.com/app/1.0/web/1.0/login/qrcode", "https://webapi.115.com/files/delete", "https://my.115.com/?ct=guide&ac=status&unexpected=1", "http://my.115.com/?ct=guide&ac=status", "https://proapi.115.com/app/chrome/downurl?other=1"} {
		r, _ := http.NewRequest("POST", url, nil)
		if _, err := tpt.RoundTrip(r); err == nil {
			t.Fatal("unexpected endpoint accepted")
		}
	}
	for _, v := range []struct{ method, url string }{{"GET", "https://my.115.com/?ct=guide&ac=status"}, {"POST", "https://proapi.115.com/app/chrome/downurl"}} {
		r, _ := http.NewRequest(v.method, v.url, nil)
		if _, err := tpt.RoundTrip(r); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("network calls=%d", calls)
	}
}
func TestJellyfinExplicitBackendAndLegacyConfig(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{{Config{URL: "http://jellyfin:8096/"}, "http://jellyfin:8096"}, {Config{URL: "http://old:8091", Protocol: "https", Host: "192.0.2.7", Port: 19096, BasePath: "/jf/"}, "https://192.0.2.7:19096/jf"}, {Config{Protocol: "http", Host: "::1", Port: 8096}, "http://[::1]:8096"}} {
		c := tc.cfg
		if err := c.Resolve(); err != nil || c.URL != tc.want {
			t.Fatalf("cfg=%+v err=%v", c, err)
		}
	}
	for _, c := range []Config{{Protocol: "ftp", Host: "localhost", Port: 8096}, {Protocol: "http", Host: "user@host", Port: 8096}, {Protocol: "http", Host: "host", Port: 0}, {URL: "http://user:pass@host"}, {Protocol: "http", Host: "host", Port: 8096, BasePath: "/../admin"}} {
		if c.Resolve() == nil {
			t.Fatal("invalid backend accepted")
		}
	}
}
func TestCMSRegistryGatesRewritingAndLegacyRequests(t *testing.T) {
	calls := 0
	b, err := NewIndependentCMSBridge([]string{"http://cms.test:9527"}, []string{"registered123"}, func(context.Context, string, string) (string, map[string]string, error) {
		calls++
		return "https://cdn.test/file.mkv", nil, nil
	}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The production registry is looked up dynamically, without materializing a library.
	registered := false
	b.eligible = func(ctx context.Context, pick string) bool { return registered && pick == "registered123" }
	g := NewGateway(nil, nil, []byte("secret"), nil)
	g.CMSLoader = func(context.Context) *CMSBridge { return b }
	r := httptest.NewRequest("POST", "http://gateway/Items/item/PlaybackInfo?api_key=TOKEN", nil)
	payload := `{"MediaSources":[{"Path":"http://cms.test:9527/d/registered123.mkv","Id":"source"}]}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}
	if err = g.rewritePlaybackInfoFor(resp, r); err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "/cms/play/") {
		t.Fatal("unregistered source rewritten")
	}
	w := httptest.NewRecorder()
	b.ServeLegacyHTTP(w, httptest.NewRequest("GET", "/d/registered123.mkv", nil))
	if w.Code != 404 || calls != 0 {
		t.Fatal("unregistered source reached resolver")
	}
	registered = true
	w = httptest.NewRecorder()
	b.ServeLegacyHTTP(w, httptest.NewRequest("HEAD", "/d/registered123.mkv?/中文.mkv", nil))
	if w.Code != 302 || calls != 1 || w.Header().Get("Location") != "https://cdn.test/file.mkv" {
		t.Fatal("registered source did not redirect")
	}
}
