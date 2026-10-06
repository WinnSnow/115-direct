package httpapi

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/local/115-direct/internal/jellyfin"
	"github.com/local/115-direct/internal/netproxy"
	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/syncer"
	"github.com/local/115-direct/internal/tmdb"
	"github.com/local/115-direct/internal/uploader"
	"github.com/local/115-direct/internal/wecom"
)

//go:embed ui
var uiFiles embed.FS

type Server struct {
	Store             *store.Store
	Pan               pan115.Provider
	Jobs              *organize.Service
	TMDB              *tmdb.Client
	Jellyfin          *jellyfin.Client
	Gateway           *jellyfin.Gateway
	WeCom             *wecom.Handler
	Auth              *Auth
	Sync              *syncer.Service
	Upload            *uploader.Service
	CMS               *jellyfin.CMSRuntime
	CMSListen         string
	MigrationReadOnly bool
	STRMRoot          string
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, securityHeaders, s.migrationGuard)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Store.Stats(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "数据库不可用")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	r.Get("/callbacks/wecom", s.WeCom.ServeHTTP)
	r.Post("/callbacks/wecom", s.WeCom.ServeHTTP)
	r.Post("/api/v1/session/login", s.login)
	r.Get("/api/v1/session", s.session)
	r.Group(func(api chi.Router) {
		api.Use(s.Auth.Middleware)
		api.Post("/api/v1/session/logout", s.logout)
		api.Post("/api/v1/account/password", s.changePassword)
		api.Get("/api/v1/dashboard", s.dashboard)
		api.Post("/api/v1/115/cookie", s.saveCookie)
		api.Post("/api/v1/115/check", s.check115)
		api.Get("/api/v1/115/open/oauth", s.openOAuthStatus)
		api.Put("/api/v1/115/open/oauth", s.saveOpenOAuth)
		api.Post("/api/v1/115/open/oauth/device", s.startOpenOAuthDevice)
		api.Post("/api/v1/115/open/oauth/device/token", s.exchangeOpenOAuthDevice)
		api.Get("/api/v1/115/account", s.account115)
		api.Post("/api/v1/115/qr", s.startQR)
		api.Get("/api/v1/115/qr/{id}", s.pollQR)
		api.Get("/api/v1/115/directories", s.directories)
		api.Get("/api/v1/files/115", s.browse115)
		api.Get("/api/v1/files/strm", s.browseSTRM)
		api.Get("/api/v1/files/strm/content", s.readSTRM)
		api.Get("/api/v1/media", s.mediaLinks)
		api.Post("/api/v1/organize/preview", s.organizePreview)
		api.Post("/api/v1/organize/execute", s.organizeExecute)
		api.Post("/api/v1/organize/template/preview", s.templatePreview)
		api.Get("/api/v1/organize/defaults", s.organizationDefaults)
		api.Get("/api/v1/media/{id}/delete-preview", s.deletionPreview)
		api.Post("/api/v1/media/{id}/delete", s.deleteMedia)
		api.Post("/api/v1/media/{id}/restore", s.restoreMedia)
		api.Get("/api/v1/deletion-reviews", s.deletionReviews)
		api.Post("/api/v1/deletion-reviews/{id}", s.resolveReview)
		api.Get("/api/v1/executions", s.executions)
		api.Post("/api/v1/executions/{id}/retry", s.retryExecution)
		api.Post("/api/v1/files/preview", s.filePreview)
		api.Post("/api/v1/files/execute", s.fileExecute)
		api.Post("/api/v1/proxy/test", s.proxyTest)
		api.Get("/api/v1/logs", s.listLogs)
		api.Get("/api/v1/settings/{key}", s.getSetting)
		api.Put("/api/v1/settings/{key}", s.putSetting)
		api.Get("/api/v1/users", s.listUsers)
		api.Post("/api/v1/users", s.createUser)
		api.Put("/api/v1/users/{username}", s.updateUser)
		api.Delete("/api/v1/users/{username}", s.deleteUser)
		api.Get("/api/v1/transfers", s.listJobs)
		api.Get("/api/v1/records/transfers", s.transferRecords)
		api.Get("/api/v1/records/sync", s.syncRecords)
		api.Get("/api/v1/uploads", s.uploads)
		api.Post("/api/v1/uploads/{id}/retry", s.retryUpload)
		api.Get("/api/v1/upload/sessions", s.uploadSessions)
		api.Post("/api/v1/upload/sessions", s.createUploadSession)
		api.Get("/api/v1/upload/sessions/{id}", s.uploadSessionGet)
		api.Head("/api/v1/upload/sessions/{id}", s.uploadSessionHead)
		api.Patch("/api/v1/upload/sessions/{id}", s.uploadSessionPatch)
		api.Post("/api/v1/upload/sessions/{id}/complete", s.uploadSessionComplete)
		api.Delete("/api/v1/upload/sessions/{id}", s.uploadSessionDelete)
		api.Get("/api/v1/upload/settings", s.uploadSettings)
		api.Put("/api/v1/upload/settings", s.putUploadSettings)
		api.Get("/api/v1/records/organization", s.organizationRecords)
		api.Post("/api/v1/records/organization/{id}/retry", s.reorganizeRecord)
		api.Get("/api/v1/recognition-cache", s.cacheEntries)
		api.Post("/api/v1/recognition-cache/{id}/delete", s.deleteCache)
		api.Get("/api/v1/classification/defaults", s.classificationDefaults)
		api.Post("/api/v1/classification/preview", s.classificationPreview)
		api.Post("/api/v1/transfers", s.createJob)
		api.Post("/api/v1/organize/jobs/{id}/confirm", s.confirmJob)
		api.Get("/api/v1/organize/jobs/{id}/preview", s.organizeJobPreview)
		api.Post("/api/v1/organize/jobs/{id}/retry", s.retryJob)
		api.Post("/api/v1/organize/library/reconcile", s.reconcileLibrary)
		api.Post("/api/v1/organize/received", s.organizeReceived)
		api.Post("/api/v1/organize/received/full", s.organizeReceivedFull)
		api.Post("/api/v1/organize/received/cleanup", s.cleanupReceived)
		api.Post("/api/v1/organize/local", s.organizeLocal)
		api.Post("/api/v1/organize/local/queue", s.queueLocalOrganize)
		api.Post("/api/v1/organize/local/queue/full", s.queueLocalOrganizeFull)
		api.Post("/api/v1/organize/library/scrape", s.scrapeLibrary)
		api.Get("/api/v1/tmdb/search", s.searchTMDB)
		api.Get("/api/v1/tmdb/details", s.tmdbDetails)
		api.Get("/api/v1/tmdb/seasons", s.tmdbSeason)
		api.Get("/api/v1/tmdb/image", s.tmdbImage)
		api.Post("/api/v1/wecom/callback-url", s.wecomCallbackURL)
		api.Post("/api/v1/wecom/menu/sync", s.syncWecomMenu)
		api.Post("/api/v1/jellyfin/test", s.testJellyfin)
		api.Get("/api/v1/cms/status", s.cmsStatus)
		api.Post("/api/v1/cms/check", s.cmsCheck)
		api.Post("/api/v1/cms/parse", s.cmsParse)
		api.Post("/api/v1/cms/preview", s.cmsPreview)
		api.Post("/api/v1/cms/import", s.cmsImport)
		api.Get("/api/v1/cms/records", s.cmsRecords)
		api.Post("/api/v1/playback/diagnose", s.diagnosePlayback)
		api.Get("/api/v1/playback/events", s.playbackEvents)
		api.Get("/api/v1/sync/status", s.syncStatus)
		api.Post("/api/v1/sync/full", s.runFullSync)
		api.Post("/api/v1/sync/incremental", s.runIncrementalSync)
	})
	r.Handle("/*", spaHandler())
	return r
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https://image.tmdb.org; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func spaHandler() http.Handler {
	sub, _ := fs.Sub(uiFiles, "ui")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, legacyPath := range []string{"/prototype-a", "/prototype-c"} {
			if r.URL.Path == legacyPath || strings.HasPrefix(r.URL.Path, legacyPath+"/") {
				http.NotFound(w, r)
				return
			}
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			w.Header().Set("Cache-Control", "no-store")
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			files.ServeHTTP(w, clone)
			return
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
		} else if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	csrf, err := s.Auth.Login(w, req.Username, req.Password)
	if err != nil {
		if s.Store != nil {
			s.Store.Audit(r.Context(), "auth", "管理台登录失败")
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
		return
	}
	if s.Store != nil {
		s.Store.Audit(r.Context(), "auth", "管理台登录成功")
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": csrf, "username": req.Username, "role": s.Auth.RoleFor(req.Username)})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	token, ok := s.Auth.session(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	identity, _ := s.Auth.CurrentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": s.Auth.csrf(token), "username": identity.Username, "role": identity.Role})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(w)
	if s.Store != nil {
		s.Store.Audit(r.Context(), "auth", "管理台已退出")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	identity, ok := s.Auth.CurrentUser(r)
	username := s.Auth.user
	if ok && identity.Username != "" {
		username = identity.Username
	}
	err := s.Auth.ChangePasswordFor(username, req.OldPassword, req.NewPassword, func(hash string) error {
		// Keep the legacy encrypted setting in sync for the administrator. Other
		// users are stored exclusively in the users table.
		if username == s.Auth.user {
			if err := s.Store.PutSetting(r.Context(), "auth", map[string]string{"password_hash": hash}); err != nil {
				return err
			}
		}
		if err := s.Store.UpdateUserPassword(r.Context(), username, hash); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	})
	if err != nil {
		s.Store.Audit(r.Context(), "auth", "管理密码修改失败")
		writeError(w, http.StatusBadRequest, "password_change_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "auth", "管理密码修改成功，已有会话已失效")
	s.Auth.Logout(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	cookieOK := s.Pan.Check(r.Context()) == nil
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "cookie_ok": cookieOK})
}

func (s *Server) saveCookie(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cookie string `json:"cookie"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.Pan.SetCookie(strings.TrimSpace(req.Cookie)); err != nil {
		writeError(w, 400, "invalid_cookie", err.Error())
		return
	}
	if err := s.Pan.Check(r.Context()); err != nil {
		writeError(w, 400, "cookie_rejected", err.Error())
		return
	}
	if err := s.Store.PutSetting(r.Context(), "pan", map[string]string{"cookie": strings.TrimSpace(req.Cookie)}); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "auth", "115 Cookie 已验证并保存")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) check115(w http.ResponseWriter, r *http.Request) {
	if err := s.Pan.Check(r.Context()); err != nil {
		writeError(w, 502, "pan_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) startQR(w http.ResponseWriter, r *http.Request) {
	session, err := s.Pan.StartQR(r.Context())
	if err != nil {
		writeError(w, 502, "qr_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}
func (s *Server) pollQR(w http.ResponseWriter, r *http.Request) {
	status, err := s.Pan.PollQR(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 502, "qr_failed", err.Error())
		return
	}
	if status.Cookie != "" {
		if err := s.Store.PutSetting(r.Context(), "pan", map[string]string{"cookie": status.Cookie}); err != nil {
			writeInternal(w, err)
			return
		}
		s.Store.Audit(r.Context(), "auth", "115 扫码登录成功")
	}
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) directories(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Query().Get("cid")
	if cid == "" {
		cid = "0"
	}
	entries, err := s.Pan.List(r.Context(), cid)
	if err != nil {
		writeError(w, 502, "pan_unavailable", err.Error())
		return
	}
	dirs := make([]pan115.Entry, 0)
	for _, entry := range entries {
		if entry.Directory {
			dirs = append(dirs, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cid": cid, "entries": dirs})
}

var settingKeys = map[string]bool{"directories": true, "tmdb": true, "wecom": true, "jellyfin": true, "sync": true, "playback": true, "organization": true, "proxy": true, "logging": true, "classification": true, "recognition": true, "cms": true}

func (s *Server) getSetting(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if !settingKeys[key] {
		writeError(w, 404, "not_found", "设置项不存在")
		return
	}
	var value map[string]any
	if err := s.Store.GetSetting(r.Context(), key, &value); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeInternal(w, err)
		return
	}
	if value == nil && s.Jobs != nil {
		switch key {
		case "directories":
			raw, _ := json.Marshal(s.Jobs.Directories(r.Context()))
			_ = json.Unmarshal(raw, &value)
		case "organization":
			raw, _ := json.Marshal(organize.DefaultOptions())
			_ = json.Unmarshal(raw, &value)
		case "proxy":
			raw, _ := json.Marshal(netproxy.Defaults())
			_ = json.Unmarshal(raw, &value)
		case "logging":
			raw, _ := json.Marshal(store.DefaultLogConfig())
			_ = json.Unmarshal(raw, &value)
		case "classification":
			raw, _ := json.Marshal(organize.DefaultClassification())
			_ = json.Unmarshal(raw, &value)
		case "recognition":
			raw, _ := json.Marshal(store.DefaultCacheConfig())
			_ = json.Unmarshal(raw, &value)
		case "sync":
			value = map[string]any{"enabled": true, "interval_minutes": 5}
		}
	}
	if key == "cms" {
		c := s.cmsConfig(r.Context())
		raw, _ := json.Marshal(c)
		_ = json.Unmarshal(raw, &value)
	}
	if key == "directories" && s.Jobs != nil {
		raw, _ := json.Marshal(s.Jobs.Directories(r.Context()))
		_ = json.Unmarshal(raw, &value)
	}
	if key == "organization" && s.Jobs != nil {
		raw, _ := json.Marshal(s.Jobs.Options(r.Context()))
		_ = json.Unmarshal(raw, &value)
	}
	if key == "proxy" {
		if value == nil {
			raw, _ := json.Marshal(netproxy.Defaults())
			_ = json.Unmarshal(raw, &value)
		}
		if value["bypass"] == nil {
			value["bypass"] = []string{}
		}
	}
	for _, secret := range []string{"token", "secret", "api_key", "encoding_aes_key", "password", "callback_access_token", "cookie"} {
		if raw, _ := value[secret].(string); raw != "" {
			value[secret] = "********"
		}
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) putSetting(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if !settingKeys[key] {
		writeError(w, 404, "not_found", "设置项不存在")
		return
	}
	var value map[string]any
	if !decodeJSON(w, r, &value) {
		return
	}
	preserveSecrets(r.Context(), s.Store, key, value)
	var validation error
	switch key {
	case "cms":
		c := jellyfin.DefaultCMSConfig()
		validation = decodeSetting(value, &c)
		c.Normalize()
		if validation == nil {
			validation = c.Validate()
		}
		if validation == nil {
			raw, _ := json.Marshal(c)
			_ = json.Unmarshal(raw, &value)
		}
	case "jellyfin":
		var c jellyfin.Config
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Resolve()
		}
		if validation == nil {
			raw, _ := json.Marshal(c)
			_ = json.Unmarshal(raw, &value)
		}
	case "wecom":
		var c wecom.Config
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Validate()
		}
	case "directories":
		var c organize.DirectoryConfig
		validation = decodeSetting(value, &c)
		if validation == nil && s.Jobs != nil {
			if c.PendingPath == "" {
				c.PendingPath = s.Jobs.Defaults.PendingPath
			}
			validation = organize.ValidateRoots(c)
			if validation == nil {
				old := s.Jobs.Directories(r.Context())
				links, err := s.Store.ListLinks(r.Context())
				if err != nil {
					validation = err
				} else if len(links) > 0 && (old.STRMPath != c.STRMPath || old.PendingPath != c.PendingPath) {
					validation = errors.New("已有文件关联；根目录变更需先预览迁移，默认不批量移动")
				}
			}
		}
		if validation == nil && (c.InboxCID == c.LibraryCID || c.InboxCID == "" || c.LibraryCID == "") {
			validation = errors.New("115接收与分类目录必须独立配置")
		}
		value["pending_path"] = c.PendingPath
	case "organization":
		var o organize.Options
		validation = decodeSetting(value, &o)
		o.Normalize()
		if validation == nil {
			validation = o.Validate()
		}
		if validation == nil {
			raw, _ := json.Marshal(o)
			_ = json.Unmarshal(raw, &value)
		}
	case "proxy":
		var c netproxy.Config
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Validate()
			if c.Bypass == nil {
				c.Bypass = []string{}
			}
			value["bypass"] = c.Bypass
		}
	case "logging":
		var c store.LogConfig
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Validate()
		}
	case "classification":
		var c organize.ClassificationConfig
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Validate()
		}
	case "recognition":
		c := store.DefaultCacheConfig()
		validation = decodeSetting(value, &c)
		if validation == nil {
			validation = c.Validate()
		}
	case "sync":
		var c syncer.Config
		validation = decodeSetting(value, &c)
		if validation == nil && (c.IntervalMinutes < 1 || c.IntervalMinutes > 1440) {
			validation = errors.New("同步间隔须为1至1440分钟")
		}
	}
	if validation != nil {
		writeError(w, 400, "invalid_setting", validation.Error())
		return
	}
	if key == "playback" {
		raw, _ := json.Marshal(value)
		var options jellyfin.PlaybackOptions
		if err := json.Unmarshal(raw, &options); err != nil {
			writeError(w, 400, "invalid_playback_options", "播放设置格式无效")
			return
		}
		options.PublicURL = strings.TrimSpace(options.PublicURL)
		if err := options.Validate(); err != nil {
			writeError(w, 400, "invalid_playback_options", err.Error())
			return
		}
		value = map[string]any{"public_url": options.PublicURL, "check_link": options.CheckLink}
	}
	preserveSecrets(r.Context(), s.Store, key, value)
	if err := s.Store.PutSetting(r.Context(), key, value); err != nil {
		writeInternal(w, err)
		return
	}
	if key == "cms" && s.CMS != nil {
		s.CMS.Invalidate()
	}
	s.Store.Audit(r.Context(), "settings", "已更新设置："+key)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func preserveSecrets(ctx context.Context, st *store.Store, key string, value map[string]any) {
	var old map[string]any
	if st.GetSetting(ctx, key, &old) != nil {
		return
	}
	for _, field := range []string{"token", "secret", "api_key", "encoding_aes_key", "password", "callback_access_token", "cookie"} {
		if value[field] == "********" || value[field] == "" {
			if old[field] != nil {
				value[field] = old[field]
			}
		}
	}
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.ListJobs(r.Context(), 100)
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs})
}
func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		TMDBKind string `json:"tmdb_kind"`
		TMDBID   int64  `json:"tmdb_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.URL == "" {
		writeError(w, 400, "invalid_request", "分享链接不能为空")
		return
	}
	job, err := s.Jobs.Submit(r.Context(), "web", "", req.URL, req.Code, req.TMDBKind, req.TMDBID)
	if err != nil {
		writeError(w, 400, "invalid_transfer", err.Error())
		return
	}
	status := http.StatusAccepted
	if job.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, job)
}
func (s *Server) confirmJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.Jobs.Confirm(r.Context(), chi.URLParam(r, "id"), req.Kind, req.ID); err != nil {
		writeError(w, 400, "confirm_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "transfer", "已确认 TMDB 匹配："+chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.Retry(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, 400, "retry_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "transfer", "已重新排队："+chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (s *Server) reconcileLibrary(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.ReconcileLibrary(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "reconcile_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) organizeReceived(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.QueueReceived(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "received_organize_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "receive", fmt.Sprintf("手动整理接收目录：排队%d，跳过%d，待确认%d", result.Queued, result.Skipped, result.NeedsConfirmation))
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) organizeReceivedFull(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.QueueReceivedFull(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "received_organize_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "receive", fmt.Sprintf("全量整理接收目录：排队%d，重排%d，跳过%d，待确认%d", result.Queued, result.Requeued, result.Skipped, result.NeedsConfirmation))
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) cleanupReceived(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.CleanupReceived(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "received_cleanup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) organizeLocal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	job, err := s.Jobs.SubmitLocal(r.Context(), req.Path, req.Kind, req.ID)
	if err != nil {
		writeError(w, 400, "organize_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) queueLocalOrganize(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.QueuePendingLocalMode(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusBadGateway, "local_organize_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "organize", fmt.Sprintf("增量整理本地待整理目录：排队%d，跳过%d", result.Queued, result.Skipped))
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) queueLocalOrganizeFull(w http.ResponseWriter, r *http.Request) {
	result, err := s.Jobs.QueuePendingLocalFull(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "local_organize_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "organize", fmt.Sprintf("全量整理本地媒体：排队%d，重排%d，跳过%d", result.Queued, result.Requeued, result.Skipped))
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) scrapeLibrary(w http.ResponseWriter, r *http.Request) {
	job, err := s.Jobs.SubmitScrape(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
func (s *Server) searchTMDB(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "movie" && kind != "tv" {
		writeError(w, 400, "invalid_request", "媒体类型应为电影或电视剧")
		return
	}
	if query == "" {
		writeError(w, 400, "invalid_request", "请输入搜索词")
		return
	}
	items, err := s.TMDB.Search(r.Context(), query)
	if err != nil {
		writeError(w, 502, "tmdb_failed", err.Error())
		return
	}
	if kind != "" {
		filtered := make([]tmdb.Candidate, 0, len(items))
		for _, item := range items {
			if item.Kind == kind {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) testJellyfin(w http.ResponseWriter, r *http.Request) {
	info, err := s.Jellyfin.Test(r.Context())
	if err != nil {
		s.Store.Audit(r.Context(), "jellyfin", "Jellyfin 连接测试失败："+err.Error())
		writeError(w, 502, "jellyfin_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "jellyfin", "Jellyfin 连接测试成功")
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) syncStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Sync.Status())
}
func (s *Server) runFullSync(w http.ResponseWriter, r *http.Request) {
	if err := s.Sync.Queue(true); err != nil {
		writeError(w, 409, "sync_running", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}
func (s *Server) runIncrementalSync(w http.ResponseWriter, r *http.Request) {
	if err := s.Sync.Queue(false); err != nil {
		writeError(w, 409, "sync_running", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeError(w, 400, "invalid_json", err.Error())
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func writeInternal(w http.ResponseWriter, _ error) {
	writeError(w, 500, "internal_error", "服务器内部错误")
}

func ParseInt(value string) int64 { n, _ := strconv.ParseInt(value, 10, 64); return n }
