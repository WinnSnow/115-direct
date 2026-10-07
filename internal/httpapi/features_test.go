package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func TestFeatureDefaultsRecordsAndCacheConfirmation(t *testing.T) {
	st, ctx := newFileTestStore(t), context.Background()
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Auth: auth, Jobs: organize.NewService(st, nil, nil, nil, organize.DirectoryConfig{})}
	h := s.Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if authenticated {
			r.AddCookie(login.Result().Cookies()[0])
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/records/organization", "/api/v1/records/transfers", "/api/v1/records/sync", "/api/v1/recognition-cache", "/api/v1/classification/defaults", "/api/v1/settings/classification", "/api/v1/settings/recognition"} {
		if w := call("GET", path, "", false); w.Code != 401 {
			t.Fatalf("%s unauthorized %d", path, w.Code)
		}
		if w := call("GET", path, "", true); w.Code != 200 {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := call("PUT", "/api/v1/settings/recognition", `{"enabled":true,"ttl_days":0}`, true); w.Code != 400 {
		t.Fatalf("invalid cache %d", w.Code)
	}
	if err := st.PutCached(ctx, "fixture", "match", "Matrix", []byte(`{"id":603}`)); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/v1/recognition-cache/all/delete", `{"confirm":false}`, true); w.Code != 400 {
		t.Fatalf("missing confirm %d", w.Code)
	}
	_, total, err := st.CacheEntries(ctx, store.RecordFilter{})
	if err != nil || total != 1 {
		t.Fatal("confirmation cleared cache")
	}
	if w := call("POST", "/api/v1/recognition-cache/all/delete", `{"confirm":true}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := call("GET", "/api/v1/settings/organization", "", true)
	var o organize.Options
	if err := json.Unmarshal(w.Body.Bytes(), &o); err != nil || o.Policy("movie") != "quality" || o.Policy("tv") != "keep" {
		t.Fatalf("%s %v", w.Body.String(), err)
	}
}

func TestOrganizationJobPreviewProjectsPathsModeVersionAndQuality(t *testing.T) {
	st, ctx := newFileTestStore(t), context.Background()
	job := &store.TransferJob{ID: "task-preview", Source: "web", ShareURL: "https://115.com/s/fixture", Status: "completed", Title: "功夫女足"}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	item := map[string]any{
		"media": map[string]any{"id": "play-1", "remote_id": "remote-1", "name": "source.strm"},
		"link": map[string]any{
			"remote_id": "remote-1", "source_path": "/mnt/pending/source.strm", "output_path": "/mnt/strm/功夫女足.strm",
			"mode": "hardlink", "kind": "movie", "tmdb_id": 1491920, "version_group": "movie:1491920:0:0-0:",
			"quality": map[string]any{"resolution": 2160, "hdr": "HDR10", "codec": "H.265", "audio": "AAC"},
		},
	}
	body, _ := json.Marshal(map[string]any{"item": item})
	if err := st.PutExecution(ctx, store.Execution{ID: "organize:task-preview:remote-1", Kind: "organize", Status: "completed", Body: body}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "admin", "password"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organize/jobs/task-preview/preview", nil)
	req.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	(&Server{Store: st, Auth: auth, Jobs: organize.NewService(st, nil, nil, nil, organize.DirectoryConfig{PendingPath: "/mnt/pending"})}).Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var value struct {
		Items []store.OrganizationRecord `json:"items"`
		Mode  string                     `json:"mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 || value.Mode != "hardlink" || value.Items[0].SourcePath != "/mnt/pending/source.strm" || value.Items[0].Quality.Resolution != 2160 {
		t.Fatalf("unexpected preview: %s", w.Body.String())
	}
}

type organizationCloudPan struct {
	pan115.Provider
	tree map[string][]pan115.Entry
}

func (p *organizationCloudPan) List(_ context.Context, cid string) ([]pan115.Entry, error) {
	return p.tree[cid], nil
}

func TestOrganizationJobPreviewProjects115CloudPaths(t *testing.T) {
	st, ctx := newFileTestStore(t), context.Background()
	pan := &organizationCloudPan{tree: map[string][]pan115.Entry{
		"0":       {{ID: "inbox", Name: "转存文件夹", Directory: true}, {ID: "library", Name: "媒体库", Directory: true}},
		"inbox":   {{ID: "stage", Name: "功夫女足 (2026)", Directory: true}},
		"stage":   {{ID: "source", Name: "功夫女足.2160p.mkv", Size: 1024, SHA1: "sha"}},
		"library": nil,
	}}
	job := &store.TransferJob{ID: "task-cloud", Source: "web", ShareURL: "https://115.com/s/cloud", Status: "completed", StageCID: "stage", Title: "功夫女足"}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	media := store.MediaEntry{ID: "play-cloud", RemoteID: "target", Name: "功夫女足.2160p.mkv", RemotePath: "电影/华语电影/功夫女足 (2026)/功夫女足.2160p.mkv"}
	link := store.MediaLink{RemoteID: "target", InboxID: "stage", SourcePath: "/pending/source.strm", OutputPath: "/strm/output.strm", Mode: "hardlink", VersionGroup: "movie:1491920:0:0-0:"}
	if err := st.PutMedia(ctx, media); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"item": map[string]any{"media": media, "link": link}})
	if err := st.PutExecution(ctx, store.Execution{ID: "organize:task-cloud:target", Kind: "organize", Status: "completed", Body: body}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "admin", "password"); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: st, Pan: pan, Auth: auth, Jobs: organize.NewService(st, pan, nil, nil, organize.DirectoryConfig{InboxCID: "inbox", LibraryCID: "library"})}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organize/jobs/task-cloud/preview", nil)
	req.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var value struct {
		Items []store.OrganizationRecord `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 {
		t.Fatalf("unexpected items: %s", w.Body.String())
	}
	item := value.Items[0]
	if item.CloudSourcePath != "转存文件夹/功夫女足 (2026)/功夫女足.2160p.mkv" || item.CloudTargetPath != "媒体库/电影/华语电影/功夫女足 (2026)/功夫女足.2160p.mkv" || item.CloudOperation != "copy" {
		t.Fatalf("unexpected cloud projection: %+v", item)
	}
}

func TestOrganizationJobPreviewProjects115PathsForManualJob(t *testing.T) {
	st, ctx := newFileTestStore(t), context.Background()
	pan := &organizationCloudPan{tree: map[string][]pan115.Entry{
		"0":     {{ID: "inbox", Name: "转存文件夹", Directory: true}, {ID: "library", Name: "媒体库", Directory: true}},
		"inbox": {{ID: "stage", Name: "功夫女足 (2026)", Directory: true}},
		"stage": {{ID: "source", Name: "功夫女足.2160p.mkv", Size: 1024, SHA1: "sha"}},
	}}
	job := &store.TransferJob{ID: "manual-cloud", Source: "manual", Status: "completed", Title: "功夫女足"}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	media := store.MediaEntry{ID: "play-cloud", RemoteID: "source", Name: "功夫女足.2160p.mkv", RemotePath: "电影/华语电影/功夫女足 (2026)/功夫女足 - 2160P.strm"}
	link := store.MediaLink{RemoteID: "source", InboxID: "stage", SourcePath: "/pending/source.strm", OutputPath: "/strm/电影/华语电影/功夫女足 (2026)/功夫女足.strm", Mode: "hardlink", VersionGroup: "movie:1491920:0:0-0:"}
	if err := st.PutMedia(ctx, media); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"item": map[string]any{"media": media, "link": link}})
	if err := st.PutExecution(ctx, store.Execution{ID: "organize:manual-cloud:source", Kind: "organize", Status: "completed", Body: body}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "admin", "password"); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: st, Pan: pan, Auth: auth, Jobs: organize.NewService(st, pan, nil, nil, organize.DirectoryConfig{InboxCID: "inbox", LibraryCID: "library", STRMPath: "/strm"})}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organize/jobs/manual-cloud/preview", nil)
	req.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var value struct {
		Items []store.OrganizationRecord `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 {
		t.Fatalf("unexpected items: %s", w.Body.String())
	}
	item := value.Items[0]
	if item.CloudSourcePath != "转存文件夹/功夫女足 (2026)/功夫女足.2160p.mkv" || item.CloudTargetPath != "媒体库/电影/华语电影/功夫女足 (2026)/功夫女足 - 2160P.strm" || item.CloudOperation != "copy" {
		t.Fatalf("unexpected manual cloud projection: %+v", item)
	}
}

func TestOrganizationRetryAcceptsEncodedRecordIDAndQueuesOneFile(t *testing.T) {
	st, ctx := newFileTestStore(t), context.Background()
	pending := t.TempDir()
	cfg := organize.DirectoryConfig{PendingPath: pending, STRMPath: t.TempDir()}
	if err := st.PutSetting(ctx, "directories", cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMedia(ctx, store.MediaEntry{ID: "play-stable", RemoteID: "remote", Name: "unknown.strm", STRMPath: filepath.Join(pending, "unknown.strm")}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, store.MediaLink{RemoteID: "remote", SourcePath: filepath.Join(pending, "unknown.strm")}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: st, Auth: auth, Jobs: organize.NewService(st, nil, nil, nil, cfg)}
	req := httptest.NewRequest("POST", "/api/v1/records/organization/media%3Aremote/retry", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	req.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	jobs, err := st.OrganizationJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	var selected []string
	_ = json.Unmarshal(jobs[0].Expected, &selected)
	if len(selected) != 1 || selected[0] != "remote" {
		t.Fatal("retry selected wrong file", selected)
	}
}

func TestClassificationSaveAndPreviewDoNotRequire115Cookie(t *testing.T) {
	st := newFileTestStore(t)
	pan := pan115.New()
	if pan.Check(context.Background()) == nil {
		t.Fatal("fixture unexpectedly has a 115 cookie")
	}
	tmdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/603" {
			t.Errorf("unexpected TMDB path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 603, "title": "黑客帝国", "original_language": "en"})
	}))
	defer tmdbServer.Close()
	client := tmdb.New(func(context.Context) (string, error) { return "fixture-token", nil })
	client.BaseURL = tmdbServer.URL
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Pan: pan, Auth: auth, TMDB: client, Jobs: organize.NewService(st, pan, client, nil, organize.DirectoryConfig{})}
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	config := organize.DefaultClassification()
	for _, test := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/v1/settings/classification", config},
		{"POST", "/api/v1/classification/preview", map[string]any{"kind": "movie", "tmdb_id": 603, "config": config}},
	} {
		raw, _ := json.Marshal(test.body)
		r := httptest.NewRequest(test.method, test.path, bytes.NewReader(raw))
		r.AddCookie(login.Result().Cookies()[0])
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s returned %d: %s", test.path, w.Code, w.Body.String())
		}
	}
	if pan.Check(context.Background()) == nil {
		t.Fatal("configuration unexpectedly changed 115 login")
	}
}
