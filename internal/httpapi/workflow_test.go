package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
)

func TestUpgradeMutationRoutesRequireSessionAndCSRF(t *testing.T) {
	st := newFileTestStore(t)
	auth, err := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Auth: auth, Jobs: organize.NewService(st, nil, nil, nil, organize.DirectoryConfig{STRMPath: t.TempDir(), PendingPath: t.TempDir()})}
	h := s.Handler()
	login := httptest.NewRecorder()
	_, err = auth.Login(login, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/records/organization/media%3Aone/retry", "/api/v1/organize/preview", "/api/v1/organize/execute", "/api/v1/files/preview", "/api/v1/files/execute", "/api/v1/media/one/delete", "/api/v1/media/one/restore", "/api/v1/executions/one/retry", "/api/v1/deletion-reviews/one", "/api/v1/proxy/test", "/api/v1/classification/preview", "/api/v1/recognition-cache/all/delete", "/api/v1/sync/full", "/api/v1/sync/incremental", "/api/v1/organize/received", "/api/v1/organize/received/full", "/api/v1/organize/local/queue", "/api/v1/organize/local/queue/full"} {
		for _, cookie := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
			if cookie {
				req.AddCookie(login.Result().Cookies()[0])
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			want := 401
			if cookie {
				want = 403
			}
			if w.Code != want {
				t.Errorf("%s: %d want %d", path, w.Code, want)
			}
		}
	}
}

func TestProxySettingsAlwaysReturnBypassArray(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		value map[string]any
		want  int
	}{
		{name: "defaults"},
		{name: "legacy missing", value: map[string]any{"enabled": false}},
		{name: "legacy null", value: map[string]any{"bypass": nil}},
		{name: "existing rules", value: map[string]any{"bypass": []string{"localhost"}}, want: 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := newFileTestStore(t)
			if fixture.value != nil {
				if err := st.PutSetting(context.Background(), "proxy", fixture.value); err != nil {
					t.Fatal(err)
				}
			}
			auth, err := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{Store: st, Auth: auth}
			login := httptest.NewRecorder()
			csrf, err := auth.Login(login, "admin", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			h := s.Handler()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/proxy", nil)
			req.AddCookie(login.Result().Cookies()[0])
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			var value map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			rules, ok := value["bypass"].([]any)
			if w.Code != http.StatusOK || !ok || len(rules) != fixture.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			req = httptest.NewRequest(http.MethodPut, "/api/v1/settings/proxy", bytes.NewBufferString(`{"enabled":false,"bypass":null}`))
			req.AddCookie(login.Result().Cookies()[0])
			req.Header.Set("X-CSRF-Token", csrf)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("save status=%d body=%s", w.Code, w.Body.String())
			}
			if err := st.GetSetting(context.Background(), "proxy", &value); err != nil {
				t.Fatal(err)
			}
			if rules, ok := value["bypass"].([]any); !ok || len(rules) != 0 {
				t.Fatalf("saved bypass=%#v", value["bypass"])
			}
		})
	}
}
