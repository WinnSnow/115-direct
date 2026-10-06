package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/local/115-direct/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	identity, ok := s.Auth.CurrentUser(r)
	if !ok || identity.Role != "admin" {
		writeError(w, http.StatusForbidden, "admin_required", "需要管理员权限")
		return false
	}
	return true
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func validateUserPassword(password string) error {
	if len([]rune(password)) < 8 {
		return errors.New("密码至少需要 8 个字符")
	}
	if len(password) > 72 {
		return errors.New("密码不能超过 72 字节")
	}
	return nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateUserPassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role != "admin" && role != "operator" && role != "viewer" {
		writeError(w, http.StatusBadRequest, "invalid_role", "角色必须是 admin、operator 或 viewer")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeInternal(w, err)
		return
	}
	if err := s.Store.CreateUser(r.Context(), store.User{Username: strings.TrimSpace(req.Username), PasswordHash: string(hash), Role: role, Enabled: true}); err != nil {
		writeError(w, http.StatusBadRequest, "user_create_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "auth", "创建用户 "+strings.TrimSpace(req.Username)+"（"+role+"）")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	username := chi.URLParam(r, "username")
	user, err := s.Store.GetUser(r.Context(), username)
	if err != nil {
		writeError(w, http.StatusNotFound, "user_not_found", "用户不存在")
		return
	}
	var req struct {
		Role     string `json:"role"`
		Enabled  *bool  `json:"enabled"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = user.Role
	}
	if req.Role != "admin" && req.Role != "operator" && req.Role != "viewer" {
		writeError(w, http.StatusBadRequest, "invalid_role", "角色必须是 admin、operator 或 viewer")
		return
	}
	enabled := user.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if user.Role == "admin" && (req.Role != "admin" || !enabled) {
		count, countErr := s.enabledAdminCount(r)
		if countErr != nil {
			writeInternal(w, countErr)
			return
		}
		if count <= 1 {
			writeError(w, http.StatusBadRequest, "last_admin", "至少需要保留一个启用的管理员")
			return
		}
	}
	var passwordHash string
	if req.Password != "" {
		if err := validateUserPassword(req.Password); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
			return
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if hashErr != nil {
			writeInternal(w, hashErr)
			return
		}
		passwordHash = string(hash)
	}
	if err := s.Store.UpdateUser(r.Context(), username, req.Role, enabled); err != nil {
		writeInternal(w, err)
		return
	}
	if passwordHash != "" {
		if err := s.Store.UpdateUserPassword(r.Context(), username, passwordHash); err != nil {
			writeInternal(w, err)
			return
		}
		if username == s.Auth.user {
			if err := s.Store.PutSetting(r.Context(), "auth", map[string]string{"password_hash": passwordHash}); err != nil {
				writeInternal(w, err)
				return
			}
			if err := s.Auth.LoadPasswordHash(passwordHash); err != nil {
				writeInternal(w, err)
				return
			}
		}
	}
	s.Store.Audit(r.Context(), "auth", "更新用户 "+username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	username := chi.URLParam(r, "username")
	identity, _ := s.Auth.CurrentUser(r)
	if username == identity.Username {
		writeError(w, http.StatusBadRequest, "self_delete", "不能删除当前登录用户")
		return
	}
	user, err := s.Store.GetUser(r.Context(), username)
	if err != nil {
		writeError(w, http.StatusNotFound, "user_not_found", "用户不存在")
		return
	}
	if user.Role == "admin" && user.Enabled {
		count, countErr := s.enabledAdminCount(r)
		if countErr != nil {
			writeInternal(w, countErr)
			return
		}
		if count <= 1 {
			writeError(w, http.StatusBadRequest, "last_admin", "至少需要保留一个启用的管理员")
			return
		}
	}
	if err := s.Store.DeleteUser(r.Context(), username); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "auth", "删除用户 "+username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) enabledAdminCount(r *http.Request) (int, error) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		return 0, err
	}
	count := 0
	for _, user := range users {
		if user.Enabled && user.Role == "admin" {
			count++
		}
	}
	return count, nil
}
