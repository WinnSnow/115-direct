package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type reconcilePan struct {
	children map[string][]pan115.Entry
	next     int
	moves    []string
	deletes  []string
	renames  map[string]string
}

func (p *reconcilePan) SetCookie(string) error                                   { return nil }
func (p *reconcilePan) Check(context.Context) error                              { return nil }
func (p *reconcilePan) StartQR(context.Context) (*pan115.QRSession, error)       { return nil, nil }
func (p *reconcilePan) PollQR(context.Context, string) (*pan115.QRStatus, error) { return nil, nil }
func (p *reconcilePan) List(_ context.Context, id string) ([]pan115.Entry, error) {
	return append([]pan115.Entry(nil), p.children[id]...), nil
}
func (p *reconcilePan) Mkdir(_ context.Context, parent, name string) (string, error) {
	p.next++
	id := fmt.Sprintf("dir-%d", p.next)
	p.children[parent] = append(p.children[parent], pan115.Entry{ID: id, ParentID: parent, Name: name, Directory: true})
	return id, nil
}
func (p *reconcilePan) Move(_ context.Context, target string, ids ...string) error {
	for _, id := range ids {
		for parent, entries := range p.children {
			for i, entry := range entries {
				if entry.ID != id {
					continue
				}
				p.children[parent] = append(entries[:i], entries[i+1:]...)
				entry.ParentID = target
				p.children[target] = append(p.children[target], entry)
				p.moves = append(p.moves, id+"->"+target)
				break
			}
		}
	}
	return nil
}
func (p *reconcilePan) SnapshotShare(context.Context, string, string) (*pan115.ShareSnapshot, error) {
	return nil, nil
}
func (p *reconcilePan) ReceiveShare(context.Context, *pan115.ShareSnapshot, string, string) error {
	return nil
}
func (p *reconcilePan) Copy(context.Context, string, ...string) error { return nil }
func (p *reconcilePan) Rename(_ context.Context, id, name string) error {
	if p.renames == nil {
		p.renames = map[string]string{}
	}
	p.renames[id] = name
	for parent, entries := range p.children {
		for i := range entries {
			if entries[i].ID == id {
				p.children[parent][i].Name = name
			}
		}
	}
	return nil
}
func (p *reconcilePan) Delete(_ context.Context, ids ...string) error {
	for _, id := range ids {
		p.deletes = append(p.deletes, id)
		for parent, entries := range p.children {
			for i, entry := range entries {
				if entry.ID == id {
					p.children[parent] = append(entries[:i], entries[i+1:]...)
					break
				}
			}
		}
		delete(p.children, id)
	}
	return nil
}
func (p *reconcilePan) DownloadURL(context.Context, string, string) (string, map[string]string, error) {
	return "", nil, nil
}

func reconcileStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	vault, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDetailsByIDFallsBackToTV(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/movie/229192":
			http.Error(w, `{"status_message":"not found"}`, http.StatusNotFound)
		case "/tv/229192":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 229192, "name": "沧元图", "original_name": "The Demon Hunter",
				"original_language": "zh", "first_air_date": "2023-06-18",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := tmdb.New(func(context.Context) (string, error) { return "token", nil })
	client.BaseURL = server.URL
	service := &Service{TMDB: client}
	details, err := service.detailsByID(context.Background(), 229192, "")
	if err != nil {
		t.Fatal(err)
	}
	if details.Kind != "tv" || details.ID != 229192 || details.Title != "沧元图" {
		t.Fatalf("unexpected details: %#v", details)
	}
	if len(paths) != 2 || paths[0] != "/movie/229192" || paths[1] != "/tv/229192" {
		t.Fatalf("unexpected lookup order: %#v", paths)
	}
}

func TestReconcileLibraryCreatesTaxonomyAndMovesLegacyTitles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/1087192" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1087192, "title": "新·驯龙高手", "original_title": "How to Train Your Dragon",
			"original_language": "en", "release_date": "2025-06-06",
			"production_countries": []map[string]string{{"iso_3166_1": "US"}},
		})
	}))
	defer server.Close()
	client := tmdb.New(func(context.Context) (string, error) { return "token", nil })
	client.BaseURL = server.URL
	pan := &reconcilePan{children: map[string][]pan115.Entry{
		"library": {{ID: "movies", ParentID: "library", Name: "电影", Directory: true}},
		"movies":  {{ID: "legacy", ParentID: "movies", Name: "新·驯龙高手 (2025) {tmdbid-1087192}", Directory: true}},
	}}
	st := reconcileStore(t)
	if err := st.PutSetting(context.Background(), "directories", DirectoryConfig{LibraryCID: "library"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(st, pan, client, nil, DirectoryConfig{})
	result, err := service.ReconcileLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 11 || result.Moved != 1 || result.Skipped != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	var westernID string
	for _, entry := range pan.children["movies"] {
		if entry.Name == "欧美电影" {
			westernID = entry.ID
		}
		if entry.ID == "legacy" {
			t.Fatal("legacy title was not moved")
		}
	}
	if westernID == "" || len(pan.children[westernID]) != 1 || pan.children[westernID][0].ID != "legacy" {
		t.Fatalf("legacy title missing from western category: %#v", pan.children[westernID])
	}
	result, err = service.ReconcileLibrary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Moved != 0 || result.Skipped != 0 {
		t.Fatalf("second reconciliation was not idempotent: %#v", result)
	}
}

func TestNormalizeStagePromotesSingleReceivedDirectory(t *testing.T) {
	ctx := context.Background()
	pan := &reconcilePan{children: map[string][]pan115.Entry{
		"inbox":   {{ID: "wrapper", ParentID: "inbox", Name: "20260929-135610-job123", Directory: true}},
		"wrapper": {{ID: "received", ParentID: "wrapper", Name: "沧元图 (2023)", Directory: true}},
	}}
	st := reconcileStore(t)
	job := &store.TransferJob{ID: "job123", Status: "completed", StageCID: "wrapper"}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Status, job.StageCID = "completed", "wrapper"
	if err := st.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	service := NewService(st, pan, nil, nil, DirectoryConfig{})
	if err := service.normalizeStage(ctx, DirectoryConfig{InboxCID: "inbox"}, job); err != nil {
		t.Fatal(err)
	}
	if job.StageCID != "received" {
		t.Fatalf("stage cid = %q", job.StageCID)
	}
	if len(pan.children["inbox"]) != 1 || pan.children["inbox"][0].ID != "received" || pan.children["inbox"][0].Name != "沧元图 (2023)" {
		t.Fatalf("unexpected inbox: %#v", pan.children["inbox"])
	}
	if len(pan.deletes) != 1 || pan.deletes[0] != "wrapper" {
		t.Fatalf("wrapper was not deleted: %#v", pan.deletes)
	}
	stored, err := st.GetJob(ctx, job.ID)
	if err != nil || stored.StageCID != "received" {
		t.Fatalf("stored stage cid: %#v, err=%v", stored, err)
	}
}

func TestNormalizeStageNamesMultiRootContainer(t *testing.T) {
	ctx := context.Background()
	job := &store.TransferJob{ID: "abcdefghijk", Title: "分享 / 合集", StageCID: "wrapper"}
	pan := &reconcilePan{children: map[string][]pan115.Entry{
		"inbox": {{ID: "wrapper", ParentID: "inbox", Name: "分享 [接收中-abcdefgh]", Directory: true}},
		"wrapper": {
			{ID: "dir", ParentID: "wrapper", Name: "目录", Directory: true},
			{ID: "file", ParentID: "wrapper", Name: "说明.txt"},
		},
	}}
	service := NewService(reconcileStore(t), pan, nil, nil, DirectoryConfig{})
	if err := service.normalizeStage(ctx, DirectoryConfig{InboxCID: "inbox"}, job); err != nil {
		t.Fatal(err)
	}
	if job.StageCID != "wrapper" || pan.renames["wrapper"] != "分享 合集" || len(pan.moves) != 0 || len(pan.deletes) != 0 {
		t.Fatalf("unexpected normalization: job=%#v renames=%#v moves=%#v deletes=%#v", job, pan.renames, pan.moves, pan.deletes)
	}
}

func TestTemporaryStageNameUsesIncomingRoot(t *testing.T) {
	snapshot := &pan115.ShareSnapshot{Title: "分享标题", Entries: []pan115.Entry{{Name: "原始目录", Directory: true}}}
	if got := temporaryStageName(snapshot, "abcdefghijk"); got != "原始目录 [接收中-abcdefgh]" {
		t.Fatalf("temporary stage name = %q", got)
	}
}
