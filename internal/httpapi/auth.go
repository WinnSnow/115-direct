package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "direct_session"

type AuthUser struct {
	Username     string
	PasswordHash string
	Role         string
	Enabled      bool
}

type UserLookup func(username string) (AuthUser, error)

type AuthIdentity struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Auth struct {
	mu           sync.RWMutex
	user         string
	passwordHash []byte
	secret       []byte
	ttl          time.Duration
	secureCookie bool
	lookup       UserLookup
}

func NewAuth(user, password string, secret []byte, ttl time.Duration, secureCookie bool) (*Auth, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &Auth{user: user, passwordHash: hash, secret: secret, ttl: ttl, secureCookie: secureCookie}, nil
}

func (a *Auth) SetUserLookup(lookup UserLookup) {
	a.mu.Lock()
	a.lookup = lookup
	a.mu.Unlock()
}

func (a *Auth) LoadPasswordHash(hash string) error {
	value := []byte(hash)
	if _, err := bcrypt.Cost(value); err != nil {
		return fmt.Errorf("无效的管理密码哈希: %w", err)
	}
	a.mu.Lock()
	a.passwordHash = append([]byte(nil), value...)
	a.mu.Unlock()
	return nil
}

func (a *Auth) PasswordHash() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return string(append([]byte(nil), a.passwordHash...))
}

func (a *Auth) ChangePassword(oldPassword, newPassword string, persist func(string) error) error {
	a.mu.RLock()
	username := a.user
	a.mu.RUnlock()
	return a.ChangePasswordFor(username, oldPassword, newPassword, persist)
}

// ChangePasswordFor updates one user's password. With a user lookup configured,
// the lookup is authoritative so operator and viewer passwords remain isolated
// from the legacy administrator hash kept on Auth for compatibility.
func (a *Auth) ChangePasswordFor(username, oldPassword, newPassword string, persist func(string) error) error {
	a.mu.RLock()
	lookup := a.lookup
	defaultUser, defaultHash := a.user, append([]byte(nil), a.passwordHash...)
	a.mu.RUnlock()
	hash := defaultHash
	if lookup != nil {
		user, err := lookup(username)
		if err != nil || !user.Enabled || user.Username != username {
			return fmt.Errorf("账号不存在或已停用")
		}
		hash = []byte(user.PasswordHash)
	} else if username != defaultUser {
		return fmt.Errorf("账号不存在")
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(oldPassword)) != nil {
		return fmt.Errorf("旧密码错误")
	}
	if utf8.RuneCountInString(newPassword) < 8 {
		return fmt.Errorf("新密码至少需要 8 个字符")
	}
	if len(newPassword) > 72 {
		return fmt.Errorf("新密码不能超过 72 字节")
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(newPassword)) == nil {
		return fmt.Errorf("新密码不能与旧密码相同")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := persist(string(newHash)); err != nil {
		return err
	}
	if username == defaultUser {
		a.mu.Lock()
		a.passwordHash = newHash
		a.mu.Unlock()
	}
	return nil
}

func (a *Auth) Login(w http.ResponseWriter, user, password string) (string, error) {
	a.mu.RLock()
	lookup := a.lookup
	defaultUser, defaultHash := a.user, append([]byte(nil), a.passwordHash...)
	a.mu.RUnlock()
	var hash []byte
	valid := false
	if lookup != nil {
		candidate, err := lookup(user)
		valid = err == nil && candidate.Enabled && candidate.Username == user && bcrypt.CompareHashAndPassword([]byte(candidate.PasswordHash), []byte(password)) == nil
		if err == nil {
			hash = []byte(candidate.PasswordHash)
		}
	} else {
		valid = user == defaultUser && bcrypt.CompareHashAndPassword(defaultHash, []byte(password)) == nil
		hash = defaultHash
	}
	revision := passwordRevision(hash)
	if !valid {
		return "", fmt.Errorf("账号或密码错误")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := strings.Join([]string{user, strconv.FormatInt(time.Now().Add(a.ttl).Unix(), 10), base64.RawURLEncoding.EncodeToString(nonce), revision}, "|")
	token := payload + "|" + a.sign(payload)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(token)), Path: "/", HttpOnly: true,
		Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: int(a.ttl.Seconds())})
	return a.csrf(token), nil
}

func (a *Auth) Logout(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (a *Auth) session(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 {
		return "", false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return "", false
	}
	payload := strings.Join(parts[:4], "|")
	if !hmac.Equal([]byte(parts[4]), []byte(a.sign(payload))) {
		return "", false
	}
	a.mu.RLock()
	lookup := a.lookup
	defaultUser, defaultHash := a.user, append([]byte(nil), a.passwordHash...)
	a.mu.RUnlock()
	var currentHash []byte
	if lookup != nil {
		user, lookupErr := lookup(parts[0])
		if lookupErr != nil || !user.Enabled || user.Username != parts[0] {
			return "", false
		}
		currentHash = []byte(user.PasswordHash)
	} else {
		if parts[0] != defaultUser {
			return "", false
		}
		currentHash = defaultHash
	}
	validRevision := hmac.Equal([]byte(parts[3]), []byte(passwordRevision(currentHash)))
	if !validRevision {
		return "", false
	}
	return string(raw), true
}

func (a *Auth) passwordRevisionLocked() string {
	return passwordRevision(a.passwordHash)
}

func passwordRevision(hash []byte) string {
	digest := sha256.Sum256(hash)
	return base64.RawURLEncoding.EncodeToString(digest[:12])
}

func (a *Auth) Identity(r *http.Request) (AuthIdentity, bool) {
	token, ok := a.session(r)
	if !ok {
		return AuthIdentity{}, false
	}
	parts := strings.Split(string(token), "|")
	identity := AuthIdentity{Username: parts[0], Role: "admin"}
	a.mu.RLock()
	lookup := a.lookup
	a.mu.RUnlock()
	if lookup != nil {
		user, err := lookup(identity.Username)
		if err != nil || !user.Enabled {
			return AuthIdentity{}, false
		}
		identity.Role = user.Role
	}
	return identity, true
}

func (a *Auth) CurrentUser(r *http.Request) (AuthIdentity, bool) { return a.Identity(r) }

func (a *Auth) RoleFor(username string) string {
	a.mu.RLock()
	lookup := a.lookup
	defaultUser := a.user
	a.mu.RUnlock()
	if lookup != nil {
		user, err := lookup(username)
		if err == nil && user.Enabled && user.Role != "" {
			return user.Role
		}
	}
	if username == defaultUser {
		return "admin"
	}
	return ""
}

func (a *Auth) csrf(token string) string {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte("csrf|" + token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *Auth) sign(value string) string {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := a.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrf(token))) {
				writeError(w, http.StatusForbidden, "csrf_failed", "CSRF 校验失败")
				return
			}
			identity, identityOK := a.Identity(r)
			if identityOK && identity.Role == "viewer" && r.URL.Path != "/api/v1/session/logout" && r.URL.Path != "/api/v1/account/password" {
				writeError(w, http.StatusForbidden, "read_only_role", "只读用户不能执行写入操作")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
