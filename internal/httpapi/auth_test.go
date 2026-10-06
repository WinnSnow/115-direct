package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestAuthSessionAndCSRF(t *testing.T) {
	auth, err := NewAuth("admin", "secret", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	csrf, err := auth.Login(recorder, "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("authorized status: %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status: %d", w.Code)
	}
}

func TestSessionProbeReportsAnonymousAndAuthenticatedStates(t *testing.T) {
	auth, err := NewAuth("admin", "secret", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Auth: auth}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	w := httptest.NewRecorder()
	server.session(w, req)
	var anonymous struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&anonymous); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || anonymous.Authenticated {
		t.Fatalf("anonymous session response: status=%d body=%s", w.Code, w.Body.String())
	}

	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	req.AddCookie(login.Result().Cookies()[0])
	w = httptest.NewRecorder()
	server.session(w, req)
	var authenticated struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.NewDecoder(w.Body).Decode(&authenticated); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !authenticated.Authenticated || authenticated.CSRF != csrf {
		t.Fatalf("authenticated session response: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPasswordChangeInvalidatesSessionsAndLoadsPersistedHash(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	auth, err := NewAuth("admin", "old-password", secret, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "admin", "old-password"); err != nil {
		t.Fatal(err)
	}
	oldCookie := login.Result().Cookies()[0]

	if err := auth.ChangePassword("wrong-password", "new-password", func(string) error { return nil }); err == nil {
		t.Fatal("accepted an incorrect old password")
	}
	if err := auth.ChangePassword("old-password", "new-password", func(string) error { return errors.New("write failed") }); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if _, err := auth.Login(httptest.NewRecorder(), "admin", "old-password"); err != nil {
		t.Fatal("persistence failure changed the active password")
	}

	var persisted string
	if err := auth.ChangePassword("old-password", "new-password", func(hash string) error {
		persisted = hash
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persisted == "" || strings.Contains(persisted, "new-password") {
		t.Fatalf("unexpected persisted hash: %q", persisted)
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	oldRequest.AddCookie(oldCookie)
	if _, ok := auth.session(oldRequest); ok {
		t.Fatal("old session remained valid after password change")
	}
	if _, err := auth.Login(httptest.NewRecorder(), "admin", "old-password"); err == nil {
		t.Fatal("old password remained valid")
	}
	if _, err := auth.Login(httptest.NewRecorder(), "admin", "new-password"); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewAuth("admin", "environment-password", secret, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadPasswordHash(persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Login(httptest.NewRecorder(), "admin", "new-password"); err != nil {
		t.Fatal("persisted password was not restored")
	}
}

func TestChangePasswordHandlerPersistsEncryptedHash(t *testing.T) {
	st := newFileTestStore(t)
	auth, err := NewAuth("admin", "old-password", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: st, Auth: auth}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", strings.NewReader(`{"old_password":"old-password","new_password":"new-password"}`))
	w := httptest.NewRecorder()
	server.changePassword(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var setting struct {
		PasswordHash string `json:"password_hash"`
	}
	if err := st.GetSetting(req.Context(), "auth", &setting); err != nil {
		t.Fatal(err)
	}
	if setting.PasswordHash == "" || strings.Contains(setting.PasswordHash, "new-password") {
		t.Fatalf("unexpected stored value: %q", setting.PasswordHash)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("password change did not clear the session cookie: %#v", cookies)
	}
}

func TestAuthUserLookupSupportsRolesAndDisablesSessions(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("operator-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "environment-password", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	auth.SetUserLookup(func(username string) (AuthUser, error) {
		if username != "operator" {
			return AuthUser{}, errors.New("not found")
		}
		return AuthUser{Username: username, PasswordHash: string(hash), Role: "operator", Enabled: enabled}, nil
	})
	login := httptest.NewRecorder()
	if _, err := auth.Login(login, "operator", "operator-password"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	req.AddCookie(login.Result().Cookies()[0])
	identity, ok := auth.CurrentUser(req)
	if !ok || identity.Username != "operator" || identity.Role != "operator" {
		t.Fatalf("unexpected identity: %+v ok=%v", identity, ok)
	}
	enabled = false
	if _, ok := auth.CurrentUser(req); ok {
		t.Fatal("disabled user session remained valid")
	}
}

func TestViewerSessionsAreReadOnly(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("viewer-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "admin-password", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	auth.SetUserLookup(func(username string) (AuthUser, error) {
		if username != "viewer" {
			return AuthUser{}, errors.New("not found")
		}
		return AuthUser{Username: username, PasswordHash: string(hash), Role: "viewer", Enabled: true}, nil
	})
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "viewer", "viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers", strings.NewReader(`{}`))
	request.AddCookie(login.Result().Cookies()[0])
	request.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("viewer reached write handler") })).ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer write status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestChangePasswordForUpdatesOnlySelectedUser(t *testing.T) {
	operatorHash, err := bcrypt.GenerateFromPassword([]byte("operator-old"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth("admin", "admin-old", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	adminHash := auth.PasswordHash()
	auth.SetUserLookup(func(username string) (AuthUser, error) {
		if username == "admin" {
			return AuthUser{Username: username, PasswordHash: adminHash, Role: "admin", Enabled: true}, nil
		}
		if username == "operator" {
			return AuthUser{Username: username, PasswordHash: string(operatorHash), Role: "operator", Enabled: true}, nil
		}
		return AuthUser{}, errors.New("not found")
	})
	var persisted string
	if err := auth.ChangePasswordFor("operator", "operator-old", "operator-new", func(hash string) error {
		persisted = hash
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persisted == "" || bcrypt.CompareHashAndPassword([]byte(persisted), []byte("operator-new")) != nil {
		t.Fatal("operator password was not generated")
	}
	if _, err := auth.Login(httptest.NewRecorder(), "admin", "admin-old"); err != nil {
		t.Fatal("operator password change modified administrator password")
	}
}

func TestUserManagementRoutesRequireAdminAndProtectLastAdmin(t *testing.T) {
	st := newFileTestStore(t)
	auth, err := NewAuth("admin", "admin-password", []byte("01234567890123456789012345678901"), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureUser(context.Background(), "admin", auth.PasswordHash(), "admin"); err != nil {
		t.Fatal(err)
	}
	server := (&Server{Store: st, Auth: auth}).Handler()
	login := httptest.NewRecorder()
	csrf, err := auth.Login(login, "admin", "admin-password")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(login.Result().Cookies()[0])
		req.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}
	if w := call(http.MethodPost, "/api/v1/users", `{"username":"operator","password":"operator-password","role":"operator"}`); w.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/users", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "operator") {
		t.Fatalf("list users status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, "/api/v1/users/admin", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("last admin delete status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPut, "/api/v1/users/operator", `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("disable user status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSPAEntryDisablesCaching(t *testing.T) {
	for _, path := range []string{"/", "/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		spaHandler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d", path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("path=%s cache-control=%q", path, got)
		}
	}
}

func TestSPARemovesLegacyPrototypeRoutes(t *testing.T) {
	for _, path := range []string{"/prototype-a", "/prototype-a/dashboard", "/prototype-c", "/prototype-c/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		spaHandler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d", path, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/prototype-b", nil)
	w := httptest.NewRecorder()
	spaHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("prototype-b status=%d", w.Code)
	}
}
