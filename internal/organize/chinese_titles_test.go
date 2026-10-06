package organize

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/tmdb"
)

func TestChineseReorganizationPreservesHardlinksAndSharedMetadata(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	localized := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/247718":
			body := map[string]any{"id": 247718, "name": "MobLand", "original_name": "MobLand", "first_air_date": "2025-03-30", "original_language": "en"}
			if localized && r.URL.Query().Get("append_to_response") != "" {
				body["alternative_titles"] = map[string]any{"results": []map[string]string{{"iso_3166_1": "CN", "title": "黑帮领地"}}}
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/tv/247718/season/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"season_number": 1, "name": "第 1 季", "episodes": []map[string]any{{"episode_number": 1, "name": "第一集"}, {"episode_number": 2, "name": "第二集"}}})
		default:
			http.Error(w, "unknown", 404)
		}
	}))
	defer server.Close()
	client := tmdb.New(func(context.Context) (string, error) { return "fixture", nil })
	client.BaseURL = server.URL
	s.TMDB = client
	o := DefaultOptions()
	o.Mode = "hardlink"
	_ = s.Store.PutSetting(ctx, "organization", o)
	seedModern(t, s, c, "one", "MobLand.S01E01.2160p.mkv")
	seedModern(t, s, c, "two", "MobLand.S01E02.2160p.mkv")
	p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"one", "two"}, Kind: "tv", TMDBID: 247718})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.executePlan(ctx, c, "english", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeMetadata(ctx, c, p.Details, items); err != nil {
		t.Fatal(err)
	}
	oldTitle := p.Items[0].Link.TitlePath
	oldInode, _ := os.Stat(p.Items[0].Link.OutputPath)
	localized = true
	for _, id := range []string{"one", "two"} {
		j, err := s.ReorganizeRecord(ctx, "organize:english:"+id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.process(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
		l, err := s.Store.MediaLink(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(l.OutputPath, filepath.FromSlash("黑帮领地 (2025)/Season 01/黑帮领地 - S01E")) {
			t.Fatal("English destination", l.OutputPath)
		}
		m, _ := s.Store.GetMediaByRemoteID(ctx, id)
		if m.ID != "play-"+id {
			t.Fatal("playback ID changed")
		}
		source, _ := os.Stat(l.SourcePath)
		output, _ := os.Stat(l.OutputPath)
		if !os.SameFile(source, output) {
			t.Fatal("hardlink replaced by copy")
		}
		if id == "one" {
			if !os.SameFile(oldInode, output) {
				t.Fatal("inode changed")
			}
			if _, err := os.Stat(filepath.Join(oldTitle, "tvshow.nfo")); err != nil {
				t.Fatal("shared old metadata removed before last reference")
			}
		}
	}
	if _, err := os.Stat(oldTitle); !os.IsNotExist(err) {
		t.Fatal("empty English title retained", err)
	}
	l, _ := s.Store.MediaLink(ctx, "one")
	raw, err := os.ReadFile(filepath.Join(l.TitlePath, "tvshow.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	var nfo metadataNFO
	if err := xml.Unmarshal(raw, &nfo); err != nil || nfo.Title != "黑帮领地" || nfo.OriginalTitle != "MobLand" {
		t.Fatalf("%+v %v", nfo, err)
	}
}
