package httpapi

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/local/115-direct/internal/store"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type localEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified,omitempty"`
	Symlink   bool      `json:"symlink"`
}

func (s *Server) browse115(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		cid = "0"
	}
	entries, err := s.Pan.List(r.Context(), cid)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pan_unavailable", err.Error())
		return
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"cid": cid, "entries": entries})
}

func (s *Server) browseSTRM(w http.ResponseWriter, r *http.Request) {
	root := s.localViewRoot(r.Context(), r.URL.Query().Get("scope"))
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	path, clean, err := secureLocalPath(root, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", "STRM 路径无效")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "STRM 目录不存在")
			return
		}
		writeInternal(w, err)
		return
	}
	items := make([]localEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			known := false
			if s.Store != nil {
				links, err := s.Store.ListLinks(r.Context())
				if err != nil {
					writeInternal(w, err)
					return
				}
				for _, l := range links {
					if l.Mode == "symlink" && l.OutputPath == filepath.Join(path, entry.Name()) && l.Deleted == "" {
						known = true
						break
					}
				}
			}
			if !known {
				continue
			}
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		entryRel := filepath.Join(clean, entry.Name())
		if clean == "." {
			entryRel = entry.Name()
		}
		items = append(items, localEntry{Name: entry.Name(), Path: filepath.ToSlash(entryRel), Directory: info.IsDir(), Size: info.Size(), Modified: info.ModTime(), Symlink: entry.Type()&os.ModeSymlink != 0})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Directory != items[j].Directory {
			return items[i].Directory
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	parent := ""
	if clean != "." {
		parent = filepath.ToSlash(filepath.Dir(clean))
		if parent == "." {
			parent = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": displayLocalPath(clean), "parent": parent, "entries": items})
}

func (s *Server) readSTRM(w http.ResponseWriter, r *http.Request) {
	root := s.localViewRoot(r.Context(), r.URL.Query().Get("scope"))
	path, clean, err := secureLocalPath(root, strings.TrimSpace(r.URL.Query().Get("path")))
	if err != nil && s.Jobs != nil && r.URL.Query().Get("scope") != "pending" {
		relative := r.URL.Query().Get("path")
		if filepath.IsLocal(relative) {
			candidate := filepath.Join(root, relative)
			links, linkErr := s.Store.ListLinks(r.Context())
			if linkErr == nil {
				for _, l := range links {
					if l.OutputPath == candidate && l.Mode == "symlink" && l.Deleted == "" {
						cfg := s.Jobs.Directories(r.Context())
						real, _, e := secureLocalPath(cfg.PendingPath, strings.TrimPrefix(l.SourcePath, cfg.PendingPath+string(filepath.Separator)))
						target, e2 := filepath.EvalSymlinks(candidate)
						if e == nil && e2 == nil && real == target {
							path = candidate
							clean = relative
							err = nil
						}
					}
				}
			}
		}
	}
	if err != nil || clean == "." || !strings.EqualFold(filepath.Ext(path), ".strm") {
		writeError(w, http.StatusBadRequest, "invalid_path", "只能查看 STRM 文件")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "STRM 文件不存在")
			return
		}
		writeInternal(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		writeError(w, http.StatusBadRequest, "invalid_file", "STRM 文件不可读取")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.ToSlash(clean), "content": strings.TrimSpace(string(content)), "size": info.Size(), "modified": info.ModTime()})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	events, total, err := s.Store.QueryLogs(r.Context(), store.LogFilter{Category: strings.TrimSpace(r.URL.Query().Get("category")), Level: r.URL.Query().Get("level"), Search: r.URL.Query().Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		writeInternal(w, err)
		return
	}
	if r.URL.Query().Get("export") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="115-direct-logs.csv"`)
		out := csv.NewWriter(w)
		_ = out.Write([]string{"time", "category", "level", "message"})
		for _, e := range events {
			message := e.Message
			if strings.ContainsAny(string([]rune(message + " ")[0]), "=+-@") {
				message = "'" + message
			}
			_ = out.Write([]string{e.CreatedAt.Format(time.RFC3339), e.Category, e.Level, message})
		}
		out.Flush()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events, "total": total, "offset": offset})
}

func (s *Server) strmRoot(ctx context.Context) string {
	root := strings.TrimSpace(s.STRMRoot)
	if s.Store == nil {
		return root
	}
	var cfg struct {
		STRMPath string `json:"strm_path"`
	}
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err == nil && strings.TrimSpace(cfg.STRMPath) != "" {
		root = strings.TrimSpace(cfg.STRMPath)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return root
	}
	return root
}

func (s *Server) localViewRoot(ctx context.Context, scope string) string {
	if scope == "pending" && s.Jobs != nil {
		return s.Jobs.Directories(ctx).PendingPath
	}
	return s.strmRoot(ctx)
}

func secureLocalPath(root, relative string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", fmt.Errorf("empty root")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "" {
		clean = "."
	}
	if clean != "." && !filepath.IsLocal(clean) {
		return "", "", fmt.Errorf("path escapes root")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	candidate := filepath.Join(realRoot, clean)
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(realRoot, realCandidate)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return "", "", fmt.Errorf("path escapes root")
	}
	return realCandidate, rel, nil
}

func displayLocalPath(path string) string {
	if path == "." {
		return ""
	}
	return filepath.ToSlash(path)
}
