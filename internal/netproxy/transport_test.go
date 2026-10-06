package netproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

func TestProxyScopesCredentialsBypassAndReconfiguration(t *testing.T) {
	root := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Error("missing proxy authentication")
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	c := Defaults()
	c.Enabled = true
	c.URL = proxy.URL
	c.Username = "user"
	c.Password = "pass"
	c.BypassPrivate = false
	if err := st.PutSetting(ctx, "proxy", c); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: New(st, "tmdb")}
	resp, err := client.Get("http://example.invalid/configuration")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 || resp.StatusCode != 204 {
		t.Fatal("proxy not used")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credentials leaked")
		}
		w.WriteHeader(200)
	}))
	defer origin.Close()
	panClient := &http.Client{Transport: New(st, "pan")}
	resp, err = panClient.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("115 proxied by default")
	}
	c.BypassPrivate = true
	_ = st.PutSetting(ctx, "proxy", c)
	resp, err = client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("private address not bypassed after config change")
	}
	for _, bad := range []string{"ftp://host:21", "http://user:pass@host:80", "http://host/path", "http://host?secret=x", "garbage"} {
		if (Config{Enabled: true, URL: bad}).Validate() == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if (Config{Enabled: true, URL: "socks5://localhost:1080"}).Validate() != nil {
		t.Fatal("SOCKS5 rejected")
	}
}
