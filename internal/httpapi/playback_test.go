package httpapi

import (
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
	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

func TestPlaybackDiagnosticsRequireSessionAndCSRF(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	auth, err := NewAuth("admin", "secret", secret, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	st := newFileTestStore(t)
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "one.strm"), []byte("http://gateway/direct/media?sig="+organize.SignMedia(secret, "media")), 0600)
	_ = os.WriteFile(filepath.Join(root, "foreign.strm"), []byte("http://127.0.0.1:1234/private"), 0600)
	_ = st.PutMedia(context.Background(), store.MediaEntry{ID: "media", RemoteID: "remote", Name: "one.mp4", PickCode: "pc"})
	gateway := jellyfin.NewGateway(st, pan115.New(), secret, nil)
	s := &Server{Store: st, Auth: auth, Gateway: gateway, STRMRoot: root}
	handler := s.Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	for _, test := range []struct {
		body          string
		session, csrf bool
		status        int
	}{
		{`{"path":"one.strm"}`, false, false, 401},
		{`{"path":"one.strm"}`, true, false, 403},
		{`{"path":"../etc/passwd"}`, true, true, 400},
		{`{"path":"foreign.strm"}`, true, true, 400},
		{`{"path":"one.strm","user_agent":"bad\r\nCookie: secret"}`, true, true, 400},
		{`{"path":"one.strm"}`, true, true, 200},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/playback/diagnose", strings.NewReader(test.body))
		if test.session {
			r.AddCookie(cookie)
		}
		if test.csrf {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("body=%s status=%d response=%s", test.body, w.Code, w.Body.String())
		}
		if test.status == 200 {
			var d jellyfin.Diagnostic
			_ = json.Unmarshal(w.Body.Bytes(), &d)
			if d.Stage != "link_resolve" || d.Error == "" || d.ClientPlaybackVerified || d.RedirectReady {
				t.Fatalf("misleading diagnostic: %+v", d)
			}
		}
		if strings.Contains(w.Body.String(), organize.SignMedia(secret, "media")) {
			t.Fatal("diagnostic leaked signed STRM")
		}
	}
}

func TestPlaybackSettingsRejectInvalidPublicAddresses(t *testing.T) {
	auth, err := NewAuth("admin", "secret", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: newFileTestStore(t), Auth: auth}
	handler := s.Handler()
	login := httptest.NewRecorder()
	csrf, _ := auth.Login(login, "admin", "secret")
	for _, body := range []string{`{"public_url":"javascript:alert(1)"}`, `{"public_url":"https://user:password@example.com"}`, `{"public_url":"https://example.com?api_key=token"}`, `{"public_url":"https://example.com","check_link":"true"}`} {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/playback", strings.NewReader(body))
		r.AddCookie(login.Result().Cookies()[0])
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid settings accepted: %s status=%d", body, w.Code)
		}
	}
}
