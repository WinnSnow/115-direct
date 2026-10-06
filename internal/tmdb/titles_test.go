package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

func TestChineseAliasResolvesCachedEnglishMainTitle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("append_to_response") != "translations,alternative_titles" {
			t.Error("old primary cache was ignored")
		}
		_, _ = w.Write([]byte(`{"id":247718,"translations":{"translations":[{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":""}},{"iso_639_1":"zh","iso_3166_1":"TW","data":{"name":"黑幫領地"}}]},"alternative_titles":{"results":[{"iso_3166_1":"CN","title":"黑帮领地"},{"iso_3166_1":"CN","title":"黑帮之地"}]}}`))
	}))
	defer server.Close()
	c := New(func(context.Context) (string, error) { return "token", nil })
	c.BaseURL, c.Cache = server.URL, st
	raw := []byte(`{"id":247718,"name":"MobLand","original_name":"MobLand","first_air_date":"2025-03-30"}`)
	if err := st.PutCached(ctx, c.cacheKey("/tv/247718", url.Values{"language": {"zh-CN"}}), "details", "/tv/247718", raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		d, err := c.Details(ctx, "tv", 247718)
		if err != nil || d.Title != "黑帮领地" || d.OriginalTitle != "MobLand" {
			t.Fatalf("%+v %v", d, err)
		}
	}
	if calls != 1 {
		t.Fatalf("alias not cached: %d calls", calls)
	}
}

func TestChineseTitlePrecedenceAndFallback(t *testing.T) {
	cases := []struct{ name, kind, primary, expected, extended string }{
		{"Chinese primary", "movie", "黑客帝国", "黑客帝国", `{}`},
		{"Fresh primary replaces cached original", "tv", "MobLand", "黑帮领地", `{"id":1,"name":"黑帮领地"}`},
		{"Mainland translation before alias", "tv", "MobLand", "黑帮领地", `{"id":1,"translations":{"translations":[{"iso_639_1":"zh","iso_3166_1":"CN","data":{"name":"黑帮领地"}}]},"alternative_titles":{"results":[{"iso_3166_1":"CN","title":"黑帮之地"}]}}`},
		{"Movie alias format", "movie", "Movie", "电影中文名", `{"id":1,"alternative_titles":{"titles":[{"iso_3166_1":"CN","title":"电影中文名"}]}}`},
		{"Chinese regional translation", "tv", "MobLand", "黑幫領地", `{"id":1,"translations":{"translations":[{"iso_639_1":"zh","iso_3166_1":"TW","data":{"name":"黑幫領地"}}]}}`},
		{"No translation preserves original", "tv", "MobLand", "MobLand", `{"id":1}`},
		{"Different work rejected", "tv", "MobLand", "MobLand", `{"id":2,"alternative_titles":{"results":[{"iso_3166_1":"CN","title":"错误作品"}]}}`},
		{"Japanese original still checks Chinese", "movie", "ハウルの動く城", "哈尔的移动城堡", `{"id":1,"alternative_titles":{"titles":[{"iso_3166_1":"CN","title":"哈尔的移动城堡"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("append_to_response") != "" {
					_, _ = w.Write([]byte(tc.extended))
					return
				}
				body := map[string]any{"id": 1, "original_title": tc.primary, "original_name": tc.primary}
				if tc.kind == "movie" {
					body["title"] = tc.primary
				} else {
					body["name"] = tc.primary
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			c := New(func(context.Context) (string, error) { return "token", nil })
			c.BaseURL = server.URL
			d, err := c.Details(context.Background(), tc.kind, 1)
			if err != nil || d.Title != tc.expected {
				t.Fatalf("%+v %v", d, err)
			}
			if tc.name == "Chinese primary" && calls != 1 {
				t.Fatal("unnecessary alias request")
			}
		})
	}
}
