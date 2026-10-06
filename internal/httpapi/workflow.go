package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/local/115-direct/internal/netproxy"
	"github.com/local/115-direct/internal/organize"
)

func (s *Server) mediaLinks(w http.ResponseWriter, r *http.Request) {
	media, err := s.Store.ListMedia(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	links, err := s.Store.ListLinks(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"media": media, "links": links})
}
func (s *Server) organizePreview(w http.ResponseWriter, r *http.Request) {
	var req organize.OrganizeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.Jobs.Preview(r.Context(), req)
	if err != nil {
		writeError(w, 400, "preview_failed", err.Error())
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) organizeExecute(w http.ResponseWriter, r *http.Request) {
	var req organize.OrganizeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	j, err := s.Jobs.SubmitPlan(r.Context(), req)
	if err != nil {
		writeError(w, 400, "organize_failed", err.Error())
		return
	}
	writeJSON(w, 202, j)
}
func (s *Server) templatePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Template string            `json:"template"`
		Values   map[string]string `json:"values"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	path, err := organize.RenderTemplate(req.Template, req.Values)
	if err != nil {
		writeError(w, 400, "invalid_template", err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}
func (s *Server) organizationDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, organize.DefaultOptions())
}
func (s *Server) deletionPreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.Jobs.PreviewDelete(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "delete_preview_failed", err.Error())
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Digest string `json:"digest"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.Jobs.Delete(r.Context(), chi.URLParam(r, "id"), req.Digest); err != nil {
		writeError(w, 400, "delete_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) restoreMedia(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.Restore(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, 400, "restore_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) deletionReviews(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.Reviews(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) resolveReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != "moved" && req.Status != "ignored" {
		writeError(w, 400, "invalid_status", "明确删除请使用删除预览")
		return
	}
	if err := s.Store.ResolveReview(r.Context(), chi.URLParam(r, "id"), req.Status); err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) executions(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.Executions(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	for i := range items {
		items[i].Body = nil
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) retryExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.RetryExecution(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, 400, "retry_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) filePreview(w http.ResponseWriter, r *http.Request) {
	var req organize.FileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.Jobs.PreviewFiles(r.Context(), req)
	if err != nil {
		writeError(w, 400, "file_preview_failed", err.Error())
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) fileExecute(w http.ResponseWriter, r *http.Request) {
	var req organize.FileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	items, err := s.Jobs.ExecuteFiles(r.Context(), req)
	if err != nil {
		writeError(w, 400, "file_operation_failed", err.Error())
		return
	}
	for i := range items {
		items[i].Body = nil
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) proxyTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"`
		URL     string `json:"url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Service == "pan_direct" {
		entries, err := s.Store.ListMedia(r.Context())
		if err != nil {
			writeInternal(w, err)
			return
		}
		for _, m := range entries {
			if m.Missing != 0 || m.PickCode == "" {
				continue
			}
			start := time.Now()
			link, _, err := s.Pan.DownloadURL(r.Context(), m.PickCode, r.UserAgent())
			if err != nil {
				writeError(w, 502, "direct_test_failed", "115直链验证失败")
				return
			}
			u, err := url.Parse(link)
			if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
				writeError(w, 502, "direct_test_failed", "115返回直链无效")
				return
			}
			writeJSON(w, 200, map[string]any{"connected": true, "status": 302, "elapsed_ms": time.Since(start).Milliseconds()})
			return
		}
		writeError(w, 400, "no_media", "先同步一个媒体文件再验证115直链")
		return
	}
	allowed := map[string]string{"tmdb": "https://api.themoviedb.org/3/configuration", "images": "https://image.tmdb.org/t/p/w92/", "pan": "https://webapi.115.com/", "wecom": "https://qyapi.weixin.qq.com/", "jellyfin": ""}
	endpoint, ok := allowed[req.Service]
	if !ok {
		writeError(w, 400, "invalid_service", "服务无效")
		return
	}
	if req.Service == "jellyfin" {
		if s.Jellyfin == nil {
			writeError(w, 400, "not_configured", "Jellyfin 未配置")
			return
		}
		cfg, err := s.Jellyfin.Config(r.Context())
		if err != nil || cfg.URL == "" {
			writeError(w, 400, "not_configured", "Jellyfin 未配置")
			return
		}
		endpoint = strings.TrimRight(cfg.URL, "/") + "/System/Info/Public"
	}
	client := &http.Client{Transport: netproxy.New(s.Store, req.Service), Timeout: 15 * time.Second}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	start := time.Now()
	if err == nil {
		var response *http.Response
		response, err = client.Do(request)
		if err == nil {
			response.Body.Close()
			writeJSON(w, 200, map[string]any{"connected": true, "status": response.StatusCode, "elapsed_ms": time.Since(start).Milliseconds()})
			return
		}
	}
	writeError(w, 502, "proxy_test_failed", "代理连接测试失败")
}
func decodeSetting(value map[string]any, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
