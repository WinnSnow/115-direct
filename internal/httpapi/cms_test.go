package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/jellyfin"
)

func TestCMSInheritancePreservesFilesAndRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	st := newFileTestStore(t)
	root := t.TempDir()
	outside := t.TempDir()
	content := "http://cms.test:9527/d/registered123.mkv?/中文电影.mkv\n"
	for name, value := range map[string]string{"电影.strm": content, "电影.nfo": "USER NFO", "poster.jpg": "USER IMAGE"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(outside, "private.strm"), []byte(content), 0600)
	_ = os.Symlink(filepath.Join(outside, "private.strm"), filepath.Join(root, "outside.strm"))
	cfg := jellyfin.DefaultCMSConfig()
	cfg.Origins = []string{"http://cms.test:9527"}
	cfg.STRMRoots = []string{root}
	if err := st.PutSetting(ctx, "cms", cfg); err != nil {
		t.Fatal(err)
	}
	auth, _ := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	login := httptest.NewRecorder()
	csrf, _ := auth.Login(login, "admin", "test-password")
	s := &Server{Store: st, Auth: auth, CMS: &jellyfin.CMSRuntime{Store: st}}
	h := s.Handler()
	call := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.AddCookie(login.Result().Cookies()[0])
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("/api/v1/cms/preview", map[string]any{"root": root, "limit": 100})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ready") || !strings.Contains(w.Body.String(), "symlink_skipped") {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		w = call("/api/v1/cms/import", map[string]any{"root": root, "paths": []string{"电影.strm", "../private.strm", "outside.strm"}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"imported":1`) {
			t.Fatalf("import %s", w.Body.String())
		}
	}
	records, total, err := st.ListCMSMedia(ctx, 100, 0)
	if err != nil || total != 1 || len(records) != 1 {
		t.Fatalf("idempotent inheritance: %d %v", total, err)
	}
	for name, want := range map[string]string{"电影.strm": content, "电影.nfo": "USER NFO", "poster.jpg": "USER IMAGE"} {
		raw, _ := os.ReadFile(filepath.Join(root, name))
		if string(raw) != want {
			t.Fatal("user file modified")
		}
	}
	for _, directory := range []string{"../", outside} {
		w = call("/api/v1/cms/preview", map[string]any{"root": root, "directory": directory})
		if w.Code != 400 {
			t.Fatal("escaped directory accepted")
		}
	}
	w = call("/api/v1/cms/preview", map[string]any{"root": outside})
	if w.Code != 400 {
		t.Fatal("unconfigured root accepted")
	}
}
func TestCMSSettingsMaskCookieAndRequireCSRF(t *testing.T) {
	st := newFileTestStore(t)
	auth, _ := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	login := httptest.NewRecorder()
	csrf, _ := auth.Login(login, "admin", "test-password")
	s := &Server{Store: st, Auth: auth}
	h := s.Handler()
	r := httptest.NewRequest("PUT", "/api/v1/settings/cms", strings.NewReader(`{"enabled":false,"cookie":"PRIVATE_COOKIE","origins":null,"strm_roots":null,"interval_ms":1000}`))
	r.AddCookie(login.Result().Cookies()[0])
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/v1/settings/cms", nil)
	r.AddCookie(login.Result().Cookies()[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "PRIVATE_COOKIE") || !strings.Contains(w.Body.String(), `"cookie":"********"`) || !strings.Contains(w.Body.String(), `"origins":[]`) {
		t.Fatal("cookie or null array handling failed")
	}
	for _, path := range []string{"/api/v1/cms/preview", "/api/v1/cms/import", "/api/v1/cms/check", "/api/v1/cms/parse"} {
		r = httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.AddCookie(login.Result().Cookies()[0])
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s CSRF status %d", path, w.Code)
		}
	}
}
func TestCMSMigrationReadOnlyGuard(t *testing.T) {
	s := &Server{MigrationReadOnly: true}
	calls := 0
	h := s.migrationGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	for _, p := range []string{"/api/v1/sync/full", "/api/v1/transfers", "/api/v1/organize/execute", "/api/v1/files/execute", "/api/v1/media/id/delete", "/api/v1/upload/sessions", "/api/v1/upload/sessions/id/complete", "/api/v1/upload/settings", "/callbacks/wecom", "/api/v1/115/qr", "/api/v1/115/open/oauth/device"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 409 {
			t.Fatalf("mutation allowed: %s", p)
		}
	}
	for _, p := range []string{"/api/v1/cms/import", "/api/v1/cms/preview", "/api/v1/organize/preview", "/api/v1/jellyfin/test", "/api/v1/settings/cms"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != 200 {
			t.Fatalf("read blocked: %s", p)
		}
	}
	if calls != 5 {
		t.Fatal("blocked requests reached handlers")
	}
}
