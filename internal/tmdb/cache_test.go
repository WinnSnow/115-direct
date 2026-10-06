package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

func TestCacheReusesSuccessfulSearchAndDetailsButNotFailures(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
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
		switch r.URL.Path {
		case "/search/multi":
			fmt.Fprint(w, `{"results":[{"id":603,"media_type":"movie","title":"Matrix"}]}`)
		case "/movie/603":
			fmt.Fprint(w, `{"id":603,"title":"黑客帝国","original_language":"en"}`)
		case "/movie/1":
			fmt.Fprint(w, `{"id":999,"title":"wrong"}`)
		case "/movie/2":
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"retry"}`)
		case "/movie/3":
			fmt.Fprint(w, `{"success":false,"status_message":"error"}`)
		}
	}))
	defer server.Close()
	c := New(func(context.Context) (string, error) { return "token", nil })
	c.BaseURL, c.Cache = server.URL, st
	for i := 0; i < 2; i++ {
		if result, err := c.Search(ctx, "Matrix"); err != nil || len(result) != 1 {
			t.Fatalf("%v %v", result, err)
		}
		if result, err := c.Details(ctx, "movie", 603); err != nil || result.ID != 603 {
			t.Fatalf("%v %v", result, err)
		}
	}
	if calls != 2 {
		t.Fatalf("cache made %d requests", calls)
	}
	for _, id := range []int64{1, 2, 3} {
		for i := 0; i < 2; i++ {
			if _, err := c.Details(ctx, "movie", id); err == nil {
				t.Fatalf("accepted invalid details %d", id)
			}
		}
	}
	if calls != 8 {
		t.Fatalf("failure cached: %d calls", calls)
	}
	entries, total, err := st.CacheEntries(ctx, store.RecordFilter{})
	if err != nil || total != 2 || entries[0].Hits+entries[1].Hits != 2 {
		t.Fatalf("%v %d %v", entries, total, err)
	}
	config := store.DefaultCacheConfig()
	config.Enabled = false
	if err := st.PutSetting(ctx, "recognition", config); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, "Matrix"); err != nil {
		t.Fatal(err)
	}
	if calls != 9 {
		t.Fatal("disabled cache still used")
	}
}
