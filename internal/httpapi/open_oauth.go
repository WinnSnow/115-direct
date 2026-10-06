package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/local/115-direct/internal/pan115"
)

func (s *Server) openOAuthStatus(w http.ResponseWriter, r *http.Request) {
	var creds pan115.OpenCredentials
	if err := s.Store.GetSetting(r.Context(), "115_open_oauth", &creds); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authorized": false, "client_id": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorized": strings.TrimSpace(creds.AccessToken) != "",
		"client_id":  creds.ClientID,
		"expires_at": creds.ExpiresAt,
	})
}

func (s *Server) saveOpenOAuth(w http.ResponseWriter, r *http.Request) {
	var creds pan115.OpenCredentials
	if !decodeJSON(w, r, &creds) {
		return
	}
	if strings.TrimSpace(creds.ClientID) == "" || strings.TrimSpace(creds.AccessToken) == "" {
		writeError(w, http.StatusBadRequest, "invalid_oauth", "请填写 Open OAuth 的 client_id 和 access_token")
		return
	}
	if err := s.Store.PutSetting(r.Context(), "115_open_oauth", creds); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "auth", "115 Open OAuth 凭据已保存")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authorized": true, "client_id": creds.ClientID, "expires_at": creds.ExpiresAt})
}

func (s *Server) startOpenOAuthDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID string `json:"client_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ClientID) == "" {
		writeError(w, 400, "invalid_client", "请填写开放平台 client_id")
		return
	}
	result, err := (&pan115.OpenAuthClient{}).StartDeviceCode(r.Context(), req.ClientID)
	if err != nil {
		writeError(w, 502, "oauth_device_failed", err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) exchangeOpenOAuthDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientID   string `json:"client_id"`
		DeviceCode string `json:"device_code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.DeviceCode) == "" {
		writeError(w, 400, "invalid_device", "device_code 无效")
		return
	}
	result, err := (&pan115.OpenAuthClient{}).ExchangeDeviceCode(r.Context(), req.ClientID, req.DeviceCode)
	if err != nil {
		writeError(w, 502, "oauth_token_failed", err.Error())
		return
	}
	creds := pan115.OpenCredentials{ClientID: req.ClientID, AccessToken: result.AccessToken, RefreshToken: result.RefreshToken}
	if result.ExpiresIn > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Unix()
	}
	if err := s.Store.PutSetting(r.Context(), "115_open_oauth", creds); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "auth", "115 Open OAuth 授权成功")
	writeJSON(w, 200, map[string]any{"authorized": true, "client_id": creds.ClientID, "expires_at": creds.ExpiresAt})
}
