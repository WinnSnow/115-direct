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

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func metadataClient(t *testing.T) *tmdb.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/603":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 603, "title": "The Matrix", "original_title": "Matrix & <test>", "release_date": "1999-03-30", "overview": "Plot & <tag>", "original_language": "en", "genres": []map[string]any{{"id": 878, "name": "Science fiction"}}})
		case "/tv/229192":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 229192, "name": "The Demon Hunter", "first_air_date": "2023-06-22", "original_language": "zh", "genres": []map[string]any{{"id": 16, "name": "Animation"}}})
		case "/tv/229192/season/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "season_number": 1, "name": "Season 1", "episodes": []map[string]any{{"id": 2, "episode_number": 2, "name": "Episode Two", "overview": "Second & plot", "air_date": "2023-06-23"}}})
		default:
			t.Errorf("unexpected TMDB request %s", r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(server.Close)
	client := tmdb.New(func(context.Context) (string, error) { return "token", nil })
	client.BaseURL = server.URL
	return client
}

func TestLocalOrganizeAndScrapeNeverCalls115AndPreservesMediaID(t *testing.T) {
	ctx := context.Background()
	st := reconcileStore(t)
	root := t.TempDir()
	source := filepath.Join(root, "incoming", "The Matrix (1999)", "original.strm")
	if err := os.MkdirAll(filepath.Dir(source), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("old URL"), 0640); err != nil {
		t.Fatal(err)
	}
	cfg := DirectoryConfig{STRMPath: root, GatewayURL: "https://gateway.example"}
	if err := st.PutSetting(ctx, "directories", cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMedia(ctx, store.MediaEntry{ID: "stable", RemoteID: "remote", PickCode: "pick", Name: "The.Matrix.1999.1080p.mkv", RemotePath: "raw/original.mkv", STRMPath: source}); err != nil {
		t.Fatal(err)
	}
	// A nil provider makes any unexpected 115 call fail immediately.
	s := NewService(st, nil, metadataClient(t), []byte("secret"), cfg)
	job, err := s.SubmitLocal(ctx, "incoming/The Matrix (1999)", "movie", 603)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	media, err := st.GetMediaByRemoteID(ctx, "remote")
	if err != nil {
		t.Fatal(err)
	}
	if media.ID != "stable" || media.Name != "The.Matrix.1999.1080p.mkv" || media.RemotePath != "raw/original.mkv" {
		t.Fatalf("remote identity changed: %#v", media)
	}
	if !strings.Contains(media.STRMPath, filepath.Join("电影", "欧美电影", "The Matrix (1999) [tmdbid-603]")) {
		t.Fatal(media.STRMPath)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("old STRM retained: %v", err)
	}
	content, err := os.ReadFile(media.STRMPath)
	if err != nil || !strings.Contains(string(content), "/direct/stable?sig=") {
		t.Fatalf("STRM invalid %s %v", content, err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(media.STRMPath), "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	var nfo metadataNFO
	if err := xml.Unmarshal(data, &nfo); err != nil {
		t.Fatal(err)
	}
	if nfo.Title != "The Matrix" || nfo.Plot != "Plot & <tag>" || nfo.TMDBID != 603 || len(nfo.IDs) != 1 || nfo.IDs[0].Value != 603 {
		t.Fatalf("NFO invalid: %#v", nfo)
	}
	// Retrying after local files have moved uses the persisted selection, not the old path.
	if err := s.process(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.ScrapeLibrary(ctx)
	if err != nil || result.Titles != 1 || result.Files != 1 {
		t.Fatalf("backfill failed: %#v %v", result, err)
	}
}

func TestTVMetadataUsesSeriesAndEpisodeTMDBIDs(t *testing.T) {
	ctx := context.Background()
	st := reconcileStore(t)
	root := t.TempDir()
	client := metadataClient(t)
	details, err := client.Details(ctx, "tv", 229192)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "电视剧", "国漫", titleFolder(details), "Season 01", "The Demon Hunter - S01E02.strm")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	s := NewService(st, nil, client, nil, DirectoryConfig{STRMPath: root})
	if err := s.writeMetadata(ctx, DirectoryConfig{STRMPath: root}, details, []store.MediaEntry{{STRMPath: path}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		tag  string
		id   int64
	}{
		{filepath.Join(filepath.Dir(filepath.Dir(path)), "tvshow.nfo"), "tvshow", 229192},
		{strings.TrimSuffix(path, ".strm") + ".nfo", "episodedetails", 2},
	} {
		data, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		var nfo metadataNFO
		if err := xml.Unmarshal(data, &nfo); err != nil {
			t.Fatal(err)
		}
		if nfo.XMLName.Local != test.tag || nfo.IDs[0].Value != test.id {
			t.Fatalf("wrong provider IDs %#v", nfo)
		}
	}
}

func TestSubmitLocalRejectsTraversalAndSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	st := reconcileStore(t)
	root := t.TempDir()
	s := NewService(st, nil, nil, nil, DirectoryConfig{STRMPath: root})
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", ".", "../outside", "/tmp", "escape"} {
		if _, err := s.SubmitLocal(ctx, path, "movie", 603); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
