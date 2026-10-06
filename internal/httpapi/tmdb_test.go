package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/local/115-direct/internal/tmdb"
)

type artworkTransport func(*http.Request) (*http.Response, error)

func (f artworkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTMDBCorrectionMetadataRoutes(t *testing.T) {
	var lookups atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if r.URL.Query().Get("language") != "zh-CN" {
			t.Error("metadata language changed")
		}
		switch r.URL.Path {
		case "/search/multi":
			_, _ = w.Write([]byte(`{"results":[{"media_type":"movie","id":1,"title":"电影"},{"media_type":"tv","id":247718,"name":"黑帮领地","poster_path":"/poster.jpg","overview":"剧集简介"}]}`))
		case "/tv/247718":
			_, _ = w.Write([]byte(`{"id":247718,"name":"黑帮领地","original_name":"MobLand","first_air_date":"2025-03-30","poster_path":"/poster.jpg","backdrop_path":"/backdrop.jpg","episode_run_time":[45],"number_of_episodes":10,"seasons":[{"id":100,"season_number":0,"name":"特别篇","episode_count":1},{"id":101,"season_number":1,"name":"第 1 季","episode_count":10,"poster_path":"/season.jpg"}]}`))
		case "/tv/247718/season/1":
			_, _ = w.Write([]byte(`{"id":101,"season_number":1,"name":"第 1 季","episodes":[{"id":1001,"episode_number":10,"name":"终局","overview":"单集简介","air_date":"2025-06-01","still_path":"/still.jpg"}]}`))
		default:
			t.Errorf("unexpected lookup %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	st := newFileTestStore(t)
	client := tmdb.New(func(context.Context) (string, error) { return "fixture-token", nil })
	client.BaseURL = upstream.URL
	client.Cache = st
	imageCalls := 0
	client.ImageHTTP = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		imageCalls++
		if r.URL.Host != "image.tmdb.org" || r.URL.Path != "/t/p/w780/poster.jpg" {
			t.Fatalf("unexpected artwork target %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/gif"}}, Body: io.NopCloser(bytes.NewReader([]byte("GIF89a")))}, nil
	})}
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "admin", "password"); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Store: st, Auth: auth, TMDB: client}).Handler()
	call := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if authenticated {
			r.AddCookie(login.Result().Cookies()[0])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/tmdb/details?kind=tv&id=247718", "/api/v1/tmdb/seasons?id=247718&season=1", "/api/v1/tmdb/image?path=/poster.jpg"} {
		if w := call(path, false); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/tmdb/details?kind=person&id=1", "/api/v1/tmdb/details?kind=tv&id=0", "/api/v1/tmdb/details?kind=movie&id=bad", "/api/v1/tmdb/seasons?id=247718", "/api/v1/tmdb/seasons?id=-1&season=1", "/api/v1/tmdb/seasons?id=247718&season=-1", "/api/v1/tmdb/seasons?id=247718&season=100", "/api/v1/tmdb/image?path=https://example.com/a.jpg", "/api/v1/tmdb/image?path=/../a.jpg", "/api/v1/tmdb/search?q=test&kind=person"} {
		if w := call(path, true); w.Code != 400 {
			t.Fatalf("invalid input %s: %d", path, w.Code)
		}
	}
	if lookups.Load() != 0 {
		t.Fatal("invalid requests reached TMDB")
	}
	if imageCalls != 0 {
		t.Fatal("invalid images reached TMDB")
	}
	image := call("/api/v1/tmdb/image?path=/poster.jpg", true)
	if image.Code != 200 || image.Header().Get("Content-Type") != "image/gif" || image.Header().Get("Cache-Control") != "private, max-age=3600" || imageCalls != 1 {
		t.Fatalf("image %d %v", image.Code, image.Header())
	}
	w := call("/api/v1/tmdb/details?kind=tv&id=247718", true)
	var detail tmdb.Details
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Title != "黑帮领地" || detail.OriginalTitle != "MobLand" || len(detail.Seasons) != 2 || detail.Seasons[0].Number != 0 || detail.Seasons[1].EpisodeCount != 10 || detail.EpisodeCount != 10 || detail.Runtime != 45 || detail.BackdropPath != "/backdrop.jpg" {
		t.Fatalf("details %d: %s", w.Code, w.Body.String())
	}
	w = call("/api/v1/tmdb/seasons?id=247718&season=1", true)
	var season tmdb.SeasonDetails
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &season) != nil || len(season.Episodes) != 1 || season.Episodes[0].Number != 10 || season.Episodes[0].StillPath != "/still.jpg" {
		t.Fatalf("season %d: %s", w.Code, w.Body.String())
	}
	count := lookups.Load()
	if call("/api/v1/tmdb/details?kind=tv&id=247718", true).Code != 200 || call("/api/v1/tmdb/seasons?id=247718&season=1", true).Code != 200 || lookups.Load() != count {
		t.Fatal("metadata lookups did not use recognition cache")
	}
	w = call("/api/v1/tmdb/search?q=fixture&kind=tv", true)
	var result struct {
		Items []tmdb.Candidate `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 || result.Items[0].Kind != "tv" || result.Items[0].PosterPath == "" {
		t.Fatalf("filtered search: %s", w.Body.String())
	}
}
