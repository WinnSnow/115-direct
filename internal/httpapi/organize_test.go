package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
)

func TestLocalOrganizeRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	st := newFileTestStore(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Title"), 0750); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "test-password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Auth: auth, Jobs: organize.NewService(st, nil, nil, nil, organize.DirectoryConfig{STRMPath: root})}
	h := s.Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/v1/organize/local", "/api/v1/organize/library/scrape"} {
		for _, test := range []struct {
			cookie bool
			csrf   bool
			want   int
		}{{false, false, 401}, {true, false, 403}, {true, true, 202}} {
			r := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(`{"path":"Title","kind":"movie","id":603}`))
			if test.cookie {
				r.AddCookie(login.Result().Cookies()[0])
			}
			if test.csrf {
				r.Header.Set("X-CSRF-Token", csrf)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("%s status=%d want=%d %s", route, w.Code, test.want, w.Body.String())
			}
		}
	}
}
