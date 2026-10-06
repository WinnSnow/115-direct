package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/115-direct/internal/jellyfin"
)

func (s *Server) diagnosePlayback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Gateway == nil {
		writeError(w, 503, "gateway_unavailable", "播放网关未启动")
		return
	}
	var input struct {
		Path      string `json:"path"`
		UserAgent string `json:"user_agent"`
		CheckLink bool   `json:"check_link"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.UserAgent == "" {
		input.UserAgent = r.UserAgent()
	}
	if len(input.UserAgent) > 512 || strings.ContainsAny(input.UserAgent, "\r\n") {
		writeError(w, 400, "invalid_user_agent", "客户端 UA 格式无效")
		return
	}
	path, clean, err := secureLocalPath(s.strmRoot(r.Context()), input.Path)
	if err != nil || clean == "." || !strings.EqualFold(filepath.Ext(path), ".strm") {
		writeError(w, 400, "invalid_path", "请选择一个 STRM 文件")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "not_found", "STRM 文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		writeError(w, 400, "invalid_file", "STRM 文件不可读取")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		writeInternal(w, err)
		return
	}
	id, ok := s.Gateway.MediaID(strings.TrimSpace(string(raw)))
	if !ok {
		writeError(w, 400, "invalid_media_signature", "不是本项目生成的有效 STRM")
		return
	}
	writeJSON(w, 200, s.Gateway.Diagnose(r.Context(), id, input.UserAgent, input.CheckLink))
}

func (s *Server) playbackEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	items := []jellyfin.PlaybackEvent{}
	if s.Gateway != nil {
		items = s.Gateway.PlaybackEvents(r.URL.Query().Get("media_id"))
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
