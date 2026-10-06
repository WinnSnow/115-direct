package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDetailsIncludesClassificationMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/603" || r.URL.Query().Get("language") != "zh-CN" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 603, "title": "黑客帝国", "original_title": "The Matrix",
			"original_language": "en", "release_date": "1999-03-30",
			"origin_country":       []string{"US"},
			"production_countries": []map[string]string{{"iso_3166_1": "US"}, {"iso_3166_1": "AU"}},
			"genres":               []map[string]any{{"id": 28, "name": "动作"}, {"id": 878, "name": "科幻"}},
		})
	}))
	defer server.Close()

	client := New(func(context.Context) (string, error) { return "token", nil })
	client.BaseURL = server.URL
	details, err := client.Details(context.Background(), "movie", 603)
	if err != nil {
		t.Fatal(err)
	}
	if details.Title != "黑客帝国" || details.OriginalLanguage != "en" || details.Year != 1999 {
		t.Fatalf("unexpected details: %#v", details)
	}
	if !reflect.DeepEqual(details.OriginCountries, []string{"US", "AU"}) {
		t.Fatalf("unexpected countries: %#v", details.OriginCountries)
	}
	if !reflect.DeepEqual(details.GenreIDs, []int{28, 878}) {
		t.Fatalf("unexpected genre IDs: %#v", details.GenreIDs)
	}
}
