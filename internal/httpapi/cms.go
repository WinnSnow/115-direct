package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/local/115-direct/internal/jellyfin"
	"github.com/local/115-direct/internal/store"
)

func (s *Server) cmsConfig(ctx context.Context) jellyfin.CMSConfig {
	c := jellyfin.DefaultCMSConfig()
	_ = s.Store.GetSetting(ctx, "cms", &c)
	c.Normalize()
	return c
}
func (s *Server) cmsStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cmsConfig(r.Context())
	_, count, err := s.Store.ListCMSMedia(r.Context(), 1, 0)
	if err != nil {
		writeInternal(w, err)
		return
	}
	ready := false
	if s.CMS != nil {
		ready = s.CMS.Bridge(r.Context()) != nil
	}
	writeJSON(w, 200, map[string]any{"enabled": cfg.Enabled, "resolver_ready": ready, "legacy_enabled": cfg.LegacyEnabled, "legacy_listen": s.CMSListen, "read_only_active": s.MigrationReadOnly, "restart_required": cfg.MigrationReadOnly != s.MigrationReadOnly, "imported": count, "mode": "independent-115", "cookie_configured": cfg.Cookie != ""})
}
func (s *Server) cmsCheck(w http.ResponseWriter, r *http.Request) {
	if s.CMS == nil {
		writeError(w, 503, "not_ready", "CMS兼容模块尚未启动")
		return
	}
	if err := s.CMS.Check(r.Context()); err != nil {
		writeError(w, 400, "cookie_check_failed", err.Error())
		return
	}
	s.Store.Audit(r.Context(), "cms", "复用Cookie只读检查成功，未调用登录接口")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) cmsParse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := s.cmsConfig(r.Context())
	b, err := jellyfin.NewCMSBridge("http://127.0.0.1:1", cfg.Origins, 0, 0)
	if err != nil {
		writeError(w, 400, "invalid_config", err.Error())
		return
	}
	src, ok := b.ParseSource(strings.TrimSpace(req.URL))
	if !ok {
		writeError(w, 400, "unrecognized", "地址与已配置的旧CMS播放源不匹配")
		return
	}
	registered, err := s.Store.HasCMSPick(r.Context(), src.PickCode)
	if err != nil {
		writeInternal(w, err)
		return
	}
	// The URL query may contain private link parameters; never echo it.
	writeJSON(w, 200, map[string]any{"pick_code": src.PickCode, "extension": src.Extension, "registered": registered, "network_requests": 0})
}

type cmsScanRequest struct {
	Root      string `json:"root"`
	Directory string `json:"directory"`
	Cursor    int    `json:"cursor"`
	Limit     int    `json:"limit"`
}
type cmsScanItem struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Directory bool     `json:"directory"`
	Status    string   `json:"status"`
	PickCode  string   `json:"pick_code,omitempty"`
	Extension string   `json:"extension,omitempty"`
	NFO       bool     `json:"nfo"`
	Artwork   []string `json:"artwork"`
}

// OpenRoot confines all reads, including symlink traversal, to a configured root.
func openCMSRoot(cfg jellyfin.CMSConfig, root string) (*os.Root, error) {
	allowed := false
	for _, v := range cfg.STRMRoots {
		if filepath.Clean(root) == filepath.Clean(v) {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("目录未列入CMS继承根目录")
	}
	return os.OpenRoot(root)
}
func cmsRead(cfg jellyfin.CMSConfig, fs *os.Root, root, relative string) (store.CMSMedia, error) {
	if filepath.Ext(strings.ToLower(relative)) != ".strm" {
		return store.CMSMedia{}, fmt.Errorf("请选择STRM文件")
	}
	f, err := fs.Open(relative)
	if err != nil {
		return store.CMSMedia{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return store.CMSMedia{}, fmt.Errorf("STRM须为普通文件")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return store.CMSMedia{}, fmt.Errorf("STRM超过4096字节或读取失败")
	}
	source := strings.TrimSpace(string(raw))
	if strings.ContainsAny(source, "\r\n") {
		return store.CMSMedia{}, fmt.Errorf("STRM须为单个URL")
	}
	b, err := jellyfin.NewCMSBridge("http://127.0.0.1:1", cfg.Origins, 0, 0)
	if err != nil {
		return store.CMSMedia{}, err
	}
	src, ok := b.ParseSource(source)
	if !ok {
		return store.CMSMedia{}, fmt.Errorf("旧CMS地址未匹配")
	}
	return store.CMSMedia{Root: root, Path: filepath.Join(root, relative), Source: src.URL, PickCode: src.PickCode, Extension: src.Extension}, nil
}
func (s *Server) cmsPreview(w http.ResponseWriter, r *http.Request) {
	var req cmsScanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit < 1 || req.Limit > 500 || req.Cursor < 0 || req.Cursor > 1000000 {
		writeError(w, 400, "invalid_page", "每批1至500项，游标须为0至1000000")
		return
	}
	cfg := s.cmsConfig(r.Context())
	fs, err := openCMSRoot(cfg, req.Root)
	if err != nil {
		writeError(w, 400, "invalid_root", "继承根目录未配置或未挂载")
		return
	}
	defer fs.Close()
	dir := req.Directory
	if dir == "" {
		dir = "."
	}
	f, err := fs.Open(dir)
	if err != nil {
		writeError(w, 400, "invalid_directory", "目录越界或不可读")
		return
	}
	defer f.Close()
	// Stream a single directory. Never recursively walk the library or load it all.
	left := req.Cursor
	for left > 0 {
		n := left
		if n > 500 {
			n = 500
		}
		entries, e := f.ReadDir(n)
		left -= len(entries)
		if e != nil {
			if e == io.EOF {
				left = 0
				break
			}
			writeError(w, 400, "scan_failed", "目录读取失败")
			return
		}
	}
	entries, err := f.ReadDir(req.Limit)
	if err != nil && err != io.EOF {
		writeError(w, 400, "scan_failed", "目录读取失败")
		return
	}
	items := []cmsScanItem{}
	for _, entry := range entries {
		if r.Context().Err() != nil {
			return
		}
		rel := filepath.Join(dir, entry.Name())
		item := cmsScanItem{Path: rel, Name: entry.Name(), Directory: entry.IsDir(), Status: "skipped", Artwork: []string{}}
		if entry.Type()&os.ModeSymlink != 0 {
			item.Status = "symlink_skipped"
		} else if entry.IsDir() {
			item.Status = "directory"
		} else if strings.EqualFold(filepath.Ext(rel), ".strm") {
			media, e := cmsRead(cfg, fs, req.Root, rel)
			if e != nil {
				item.Status = "unrecognized"
			} else {
				item.Status = "ready"
				item.PickCode = media.PickCode
				item.Extension = media.Extension
			}
			base := strings.TrimSuffix(rel, filepath.Ext(rel))
			if info, e := fs.Stat(base + ".nfo"); e == nil && info.Mode().IsRegular() {
				item.NFO = true
			}
			for _, name := range []string{"movie.nfo", "tvshow.nfo"} {
				if info, e := fs.Stat(filepath.Join(dir, name)); e == nil && info.Mode().IsRegular() {
					item.NFO = true
				}
			}
			for _, name := range []string{"poster.jpg", "poster.png", "fanart.jpg", "folder.jpg", entry.Name()[:len(entry.Name())-len(filepath.Ext(entry.Name()))] + "-poster.jpg"} {
				if info, e := fs.Stat(filepath.Join(dir, name)); e == nil && info.Mode().IsRegular() {
					item.Artwork = append(item.Artwork, name)
				}
			}
		}
		items = append(items, item)
	}
	next := req.Cursor + len(entries)
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "has_more": err != io.EOF, "directory": dir, "root": req.Root, "files_modified": 0})
}
func (s *Server) cmsImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root  string   `json:"root"`
		Paths []string `json:"paths"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Paths) < 1 || len(req.Paths) > 500 {
		writeError(w, 400, "invalid_batch", "每批选择1至500个STRM")
		return
	}
	cfg := s.cmsConfig(r.Context())
	fs, err := openCMSRoot(cfg, req.Root)
	if err != nil {
		writeError(w, 400, "invalid_root", "继承根目录未配置或未挂载")
		return
	}
	defer fs.Close()
	items := []store.CMSMedia{}
	failed := []map[string]string{}
	for _, rel := range req.Paths {
		if r.Context().Err() != nil {
			return
		}
		item, e := cmsRead(cfg, fs, req.Root, rel)
		if e != nil {
			failed = append(failed, map[string]string{"path": rel, "error": "文件越界、读取失败或旧地址未匹配"})
			continue
		}
		items = append(items, item)
	}
	if err = s.Store.ImportCMSMedia(r.Context(), items); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "cms", fmt.Sprintf("只读继承STRM记录：成功%d，失败%d；未改写STRM或接管元数据所有权", len(items), len(failed)))
	writeJSON(w, 200, map[string]any{"imported": len(items), "failed": failed, "files_modified": 0})
}
func (s *Server) cmsRecords(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	items, total, err := s.Store.ListCMSMedia(r.Context(), 100, offset)
	if err != nil {
		writeInternal(w, err)
		return
	}
	// Records expose media identity and path, never source URL query parameters.
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, map[string]any{"path": item.Path, "root": item.Root, "pick_code": item.PickCode, "extension": item.Extension, "status": "inherited", "imported_at": item.ImportedAt})
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": total, "offset": offset})
}
func (s *Server) migrationGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.MigrationReadOnly && migrationBlocked(r) {
			writeError(w, 409, "migration_read_only", "当前为迁移只读模式；修改设置并重启服务后可执行整理、同步及文件修改")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func migrationBlocked(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/callbacks/wecom") {
		return r.Method != http.MethodGet
	}
	if p == "/api/v1/115/qr" || strings.HasPrefix(p, "/api/v1/115/qr/") || strings.HasPrefix(p, "/api/v1/115/open/oauth") {
		return true
	}
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return false
	}
	for _, v := range []string{"/api/v1/transfers", "/api/v1/sync/", "/api/v1/uploads/", "/api/v1/upload/", "/api/v1/executions/", "/api/v1/deletion-reviews/", "/api/v1/media/"} {
		if strings.HasPrefix(p, v) {
			return true
		}
	}
	if strings.HasPrefix(p, "/api/v1/files/") {
		return !strings.HasSuffix(p, "/preview")
	}
	if strings.HasPrefix(p, "/api/v1/organize/") {
		return !strings.HasSuffix(p, "/preview")
	}
	return false
}
