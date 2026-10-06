package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/wecom"
)

func TestWeComSettingsTokenMaskPreservationRotationAndURLAuth(t *testing.T) {
	st := newFileTestStore(t)
	auth, err := NewAuth("admin", "password", []byte("secret"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	h := (&Server{Store: st, Auth: auth}).Handler()
	call := func(method, path string, body any, session, withCSRF bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if session {
			r.AddCookie(login.Result().Cookies()[0])
		}
		if withCSRF {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	cfg := wecom.Config{Enabled: true, Token: "signature-secret", CallbackAccessToken: strings.Repeat("a", 64), CallbackBaseURL: "https://direct.example.com"}
	if w := call("PUT", "/api/v1/settings/wecom", cfg, true, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("GET", "/api/v1/settings/wecom", nil, true, false)
	var masked wecom.Config
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &masked) != nil || masked.CallbackAccessToken != "********" || masked.Token != "********" {
		t.Fatal("unmasked settings")
	}
	if w := call("PUT", "/api/v1/settings/wecom", masked, true, true); w.Code != 200 {
		t.Fatal("masked token was not preserved", w.Code, w.Body.String())
	}
	var saved wecom.Config
	if err := st.GetSetting(context.Background(), "wecom", &saved); err != nil || saved.CallbackAccessToken != cfg.CallbackAccessToken {
		t.Fatal("saved token changed")
	}
	for _, test := range []struct {
		session, csrf bool
		code          int
	}{{false, false, 401}, {true, false, 403}, {true, true, 200}} {
		w = call("POST", "/api/v1/wecom/callback-url", map[string]any{}, test.session, test.csrf)
		if w.Code != test.code {
			t.Fatal("callback URL auth", w.Code, test.code)
		}
		if w.Code == 200 && (!strings.Contains(w.Body.String(), "access_token="+cfg.CallbackAccessToken) || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("saved URL missing token or cache restriction")
		}
	}
	masked.CallbackAccessToken = strings.Repeat("b", 64)
	if w := call("PUT", "/api/v1/settings/wecom", masked, true, true); w.Code != 200 {
		t.Fatal("rotation failed")
	}
	w = call("POST", "/api/v1/wecom/callback-url", map[string]any{}, true, true)
	if !strings.Contains(w.Body.String(), "access_token="+masked.CallbackAccessToken) || strings.Contains(w.Body.String(), cfg.CallbackAccessToken) {
		t.Fatal("URL retained old token")
	}
	masked.CallbackAccessToken = "short"
	if w := call("PUT", "/api/v1/settings/wecom", masked, true, true); w.Code != 400 {
		t.Fatal("short token accepted")
	}
	logs, _, err := st.QueryLogs(context.Background(), store.LogFilter{Category: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range logs {
		if strings.Contains(entry.Message, cfg.CallbackAccessToken) || strings.Contains(entry.Message, masked.Token) {
			t.Fatal("secret in audit")
		}
	}
	for _, message := range []string{"/callbacks/wecom?access_token=ENTRYSECRET", "callback_access_token=ENTRYSECRET", "access_key=ENTRYSECRET"} {
		if strings.Contains(store.Redact(message), "ENTRYSECRET") {
			t.Fatal("callback entry secret not redacted")
		}
	}
}
