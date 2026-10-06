package httpapi

import (
	"github.com/local/115-direct/internal/wecom"
	"net/http"
)

// Reveal the saved URL only on an explicit authenticated, CSRF-checked action.
func (s *Server) wecomCallbackURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var c wecom.Config
	if err := s.Store.GetSetting(r.Context(), "wecom", &c); err != nil {
		writeError(w, 400, "wecom_not_configured", "请先保存企业微信设置")
		return
	}
	address, err := c.CallbackURL()
	if err != nil {
		writeError(w, 400, "invalid_wecom_callback", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"url": address})
}

func (s *Server) syncWecomMenu(w http.ResponseWriter, r *http.Request) {
	if s.WeCom == nil {
		writeError(w, http.StatusServiceUnavailable, "wecom_unavailable", "企业微信服务尚未就绪")
		return
	}
	if err := s.WeCom.SyncMenu(r.Context()); err != nil {
		s.Store.Audit(r.Context(), "settings", "企业微信应用菜单同步失败："+err.Error())
		writeError(w, http.StatusBadGateway, "wecom_menu_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "settings", "企业微信应用菜单已同步")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
