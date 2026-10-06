package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// Read-only metadata lookups share the configured TMDB client, proxy and cache.
func (s *Server) tmdbDetails(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	kind := r.URL.Query().Get("kind")
	if err != nil || id <= 0 || (kind != "movie" && kind != "tv") {
		writeError(w, 400, "invalid_request", "请输入有效的媒体类型和 TMDB ID")
		return
	}
	details, err := s.TMDB.Details(r.Context(), kind, id)
	if err != nil {
		writeError(w, 502, "tmdb_failed", err.Error())
		return
	}
	writeJSON(w, 200, details)
}

// Preview artwork follows the image proxy setting, without storing scraped assets.
func (s *Server) tmdbImage(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !strings.HasPrefix(path, "/") || len(path) > 256 || strings.Contains(path, "..") || strings.ContainsAny(path, "?#\\") {
		writeError(w, 400, "invalid_request", "无效的 TMDB 图片路径")
		return
	}
	data, err := s.TMDB.Image(r.Context(), path)
	if err != nil {
		writeError(w, 502, "tmdb_image_failed", err.Error())
		return
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		writeError(w, 502, "tmdb_image_failed", "图片内容无效")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) tmdbSeason(w http.ResponseWriter, r *http.Request) {
	id, idErr := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	number, seasonErr := strconv.Atoi(r.URL.Query().Get("season"))
	if idErr != nil || id <= 0 || seasonErr != nil || number < 0 || number > 99 {
		writeError(w, 400, "invalid_request", "请输入有效的 TMDB ID 和季号（0–99）")
		return
	}
	details, err := s.TMDB.Season(r.Context(), id, number)
	if err != nil {
		writeError(w, 502, "tmdb_failed", err.Error())
		return
	}
	writeJSON(w, 200, details)
}
