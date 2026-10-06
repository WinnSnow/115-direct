package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/uploader"
)

func TestResumableUploadSessionSupportsOffsetRecovery(t *testing.T) {
	st := newFileTestStore(t)
	root := t.TempDir()
	auth, err := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	upload := uploader.New(st, nil, uploader.Config{Enabled: true, LocalDir: root, TargetCID: "42", Channel: "auto", ScanInterval: 30, StableSeconds: 1})
	s := &Server{Store: st, Auth: auth, Upload: upload}
	h := s.Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	call := func(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.AddCookie(cookie)
		if method != http.MethodGet && method != http.MethodHead {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	created := call(http.MethodPost, "/api/v1/upload/sessions", []byte(`{"filename":"sample.mkv","size":11}`), nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil || session.ID == "" {
		t.Fatalf("created session=%s err=%v", created.Body.String(), err)
	}
	path := "/api/v1/upload/sessions/" + session.ID
	first := call(http.MethodPatch, path, []byte("hello"), map[string]string{"Tus-Resumable": "1.0.0", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
	if first.Code != http.StatusNoContent || first.Header().Get("Upload-Offset") != "5" {
		t.Fatalf("first patch status=%d offset=%s body=%s", first.Code, first.Header().Get("Upload-Offset"), first.Body.String())
	}
	wrong := call(http.MethodPatch, path, []byte(" world"), map[string]string{"Tus-Resumable": "1.0.0", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
	if wrong.Code != http.StatusConflict || wrong.Header().Get("Upload-Offset") != "5" {
		t.Fatalf("wrong offset status=%d offset=%s body=%s", wrong.Code, wrong.Header().Get("Upload-Offset"), wrong.Body.String())
	}
	second := call(http.MethodPatch, path, []byte(" world"), map[string]string{"Tus-Resumable": "1.0.0", "Upload-Offset": "5", "Content-Type": "application/offset+octet-stream"})
	if second.Code != http.StatusNoContent || second.Header().Get("Upload-Offset") != "11" {
		t.Fatalf("second patch status=%d offset=%s body=%s", second.Code, second.Header().Get("Upload-Offset"), second.Body.String())
	}
	completed := call(http.MethodPost, path+"/complete", []byte(`{}`), nil)
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	var final struct {
		Status     string `json:"status"`
		FinalPath  string `json:"final_path"`
		UploadTask string `json:"upload_task_id"`
	}
	if err := json.Unmarshal(completed.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != "queued" || final.UploadTask == "" || final.FinalPath == "" {
		t.Fatalf("unexpected final session=%s", completed.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(root, "sample.mkv"))
	if err != nil || string(content) != "hello world" {
		t.Fatalf("final file=%q err=%v", content, err)
	}
	items, err := st.ListUploadTasks(t.Context(), 10)
	if err != nil || len(items) != 1 || items[0].Status != "queued" {
		t.Fatalf("upload tasks=%#v err=%v", items, err)
	}
}

func TestCreateUploadSessionRejectsZeroSize(t *testing.T) {
	st := newFileTestStore(t)
	root := t.TempDir()
	auth, err := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	upload := uploader.New(st, nil, uploader.Config{Enabled: true, LocalDir: root, TargetCID: "42", Channel: "auto"})
	s := &Server{Store: st, Auth: auth, Upload: upload}
	h := s.Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/upload/sessions", bytes.NewReader([]byte(`{"filename":"empty.mkv","size":0}`)))
	r.AddCookie(login.Result().Cookies()[0])
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("zero-size upload accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestRecoverUploadSessionRetainsFinalPath(t *testing.T) {
	st := newFileTestStore(t)
	root := t.TempDir()
	final := filepath.Join(root, "ready.mkv")
	if err := os.WriteFile(final, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := &store.UploadSession{ID: "recover", OwnerUsername: "admin", Filename: "ready.mkv", SafeFilename: "ready.mkv", TotalSize: 5, ReceivedSize: 5, TargetCID: "42", Channel: "auto", TempPath: filepath.Join(root, "recover.part"), FinalPath: final, Status: "verifying", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateUploadSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	if err := s.RecoverUploadSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetUploadSession(context.Background(), "recover")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "ready" || loaded.FinalPath != final {
		t.Fatalf("session not recovered: %+v", loaded)
	}
}
