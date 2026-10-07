package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

func recordFilter(r *http.Request) store.RecordFilter {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return store.RecordFilter{Limit: limit, Offset: offset, Search: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status")}
}
func (s *Server) transferRecords(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.Store.TransferRecords(r.Context(), recordFilter(r))
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}
func (s *Server) syncRecords(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.Store.SyncRecords(r.Context(), recordFilter(r))
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}
func (s *Server) organizationRecords(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.Store.OrganizationRecords(r.Context(), recordFilter(r))
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

// organizeJobPreview is read-only. It projects the durable organization
// records for one transfer job without exposing execution bodies.
func (s *Server) organizeJobPreview(w http.ResponseWriter, r *http.Request) {
	id, err := url.PathUnescape(chi.URLParam(r, "id"))
	if err != nil || id == "" {
		writeError(w, http.StatusBadRequest, "invalid_job", "任务无效")
		return
	}
	job, err := s.Store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job_not_found", "任务不存在")
		return
	}
	items, _, err := s.Store.OrganizationRecords(r.Context(), store.RecordFilter{Search: id, Limit: 100})
	if err != nil {
		writeInternal(w, err)
		return
	}
	options := organize.DefaultOptions()
	_ = s.Store.GetSetting(r.Context(), "organization", &options)
	options.Normalize()
	mode := options.Mode
	if len(items) > 0 {
		for _, item := range items {
			if item.Mode != "" {
				mode = item.Mode
				break
			}
		}
	}
	s.addCloudOrganizationPaths(r.Context(), job, items)
	message := "确认 TMDB 条目后生成待整理和成品路径"
	if len(items) > 0 {
		message = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":    job.ID,
		"status":    job.Status,
		"title":     job.Title,
		"items":     items,
		"mode":      mode,
		"available": len(items) > 0,
		"message":   message,
	})
}

// addCloudOrganizationPaths projects the two 115 paths needed by the transfer
// page. Local filesystem paths are deliberately left to the media organization
// page. A copied file receives a new 115 ID, so source lookup uses the stable
// file name (and the direct ID when the source was not copied) rather than
// assuming the target ID is also the source ID.
func (s *Server) addCloudOrganizationPaths(ctx context.Context, job *store.TransferJob, items []store.OrganizationRecord) {
	if job == nil || len(items) == 0 || s.Jobs == nil {
		return
	}
	cfg := s.Jobs.Directories(ctx)
	if cfg.InboxCID == "" && cfg.LibraryCID == "" {
		return
	}

	rootNames := map[string]string{}
	if s.Pan != nil {
		if entries, err := s.Pan.List(ctx, "0"); err == nil {
			for _, entry := range entries {
				rootNames[entry.ID] = entry.Name
			}
		}
	}
	inboxRoot := rootNames[cfg.InboxCID]
	if inboxRoot == "" {
		inboxRoot = "接收目录"
	}
	libraryRoot := rootNames[cfg.LibraryCID]
	if libraryRoot == "" {
		libraryRoot = "整理目录"
	}

	stageName := ""
	if s.Pan != nil && cfg.InboxCID != "" && job.StageCID != "" {
		if entries, err := s.Pan.List(ctx, cfg.InboxCID); err == nil {
			for _, entry := range entries {
				if entry.ID == job.StageCID {
					stageName = entry.Name
					break
				}
			}
		}
	}

	// A manual reorganization job has no task-level StageCID. Its durable
	// organization record still carries the receive directory in InboxID, so
	// resolve each item from that bridge first and use the job StageCID for
	// ordinary transfer jobs.
	groups := map[string][]int{}
	for i := range items {
		root := items[i].InboxID
		if root == "" {
			root = job.StageCID
		}
		if root != "" && root != items[i].RemoteID {
			groups[root] = append(groups[root], i)
		}
	}
	sources := map[string]string{}
	for root, indexes := range groups {
		if s.Pan == nil {
			break
		}
		wantedNames := map[string]bool{}
		wantedIDs := map[string]bool{}
		for _, index := range indexes {
			if strings.TrimSpace(items[index].Name) != "" {
				wantedNames[items[index].Name] = true
			}
			if strings.TrimSpace(items[index].RemoteID) != "" {
				wantedIDs[items[index].RemoteID] = true
			}
		}
		byID, byName := map[string]string{}, map[string]string{}
		if err := collectCloudFiles(ctx, s.Pan, root, "", wantedNames, wantedIDs, byID, byName, map[string]bool{}); err != nil {
			continue
		}
		for _, index := range indexes {
			relative := byID[items[index].RemoteID]
			if relative == "" {
				relative = byName[items[index].Name]
			}
			if relative != "" {
				sources[items[index].ID] = relative
			}
		}
	}

	for i := range items {
		item := &items[i]
		relative := sources[item.ID]
		if relative != "" {
			root := item.InboxID
			if root == "" {
				root = job.StageCID
			}
			if s.Pan != nil {
				stageName = cloudStageName(ctx, s.Pan, cfg.InboxCID, root)
			}
			parts := []string{inboxRoot}
			if stageName != "" {
				parts = append(parts, stageName)
			}
			parts = append(parts, relative)
			item.CloudSourcePath = cloudJoin(parts...)
		}
		targetRelative := item.CloudTargetPath
		if targetRelative == "" && item.OutputPath != "" && cfg.STRMPath != "" {
			if rel, err := filepath.Rel(cfg.STRMPath, item.OutputPath); err == nil && filepath.IsLocal(rel) && rel != "." {
				targetRelative = filepath.ToSlash(rel)
			}
		}
		if targetRelative != "" {
			item.CloudTargetPath = cloudJoin(libraryRoot, targetRelative)
		}
		if item.CloudSourcePath != "" || item.CloudTargetPath != "" {
			item.CloudOperation = cloudOperation(job.Source)
		}
	}
}

func cloudStageName(ctx context.Context, provider pan115.Provider, inboxCID, stageCID string) string {
	if inboxCID == "" || stageCID == "" {
		return ""
	}
	entries, err := provider.List(ctx, inboxCID)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.ID == stageCID && entry.Directory {
			return entry.Name
		}
	}
	return ""
}

func cloudOperation(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "local", "upload":
		return "upload"
	default:
		return "copy"
	}
}

func cloudJoin(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.ReplaceAll(part, "\\", "/"), "/")
		if part != "" && part != "." {
			clean = append(clean, part)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return path.Join(clean...)
}

func collectCloudFiles(ctx context.Context, provider pan115.Provider, cid, prefix string, wantedNames, wantedIDs map[string]bool, byID, byName map[string]string, seen map[string]bool) error {
	if cid == "" || (len(wantedNames) == 0 && len(wantedIDs) == 0) || seen[cid] {
		return nil
	}
	seen[cid] = true
	entries, err := provider.List(ctx, cid)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		relative := cloudJoin(prefix, entry.Name)
		if entry.Directory {
			if err := collectCloudFiles(ctx, provider, entry.ID, relative, wantedNames, wantedIDs, byID, byName, seen); err != nil {
				return err
			}
			continue
		}
		if wantedIDs[entry.ID] {
			byID[entry.ID] = relative
			delete(wantedIDs, entry.ID)
		}
		if wantedNames[entry.Name] {
			// A receive tree can contain repeated names. Keep the first path;
			// the visible target path and file ID still let the operator verify it.
			byName[entry.Name] = relative
			delete(wantedNames, entry.Name)
		}
		if len(wantedNames) == 0 && len(wantedIDs) == 0 {
			return nil
		}
	}
	return nil
}
func (s *Server) reorganizeRecord(w http.ResponseWriter, r *http.Request) {
	id, err := url.PathUnescape(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid_record", "整理记录无效")
		return
	}
	var request struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &request) {
			return
		}
	}
	var j *store.TransferJob
	if request.Kind != "" || request.ID != 0 {
		j, err = s.Jobs.ReorganizeRecordWithMatch(r.Context(), id, request.Kind, request.ID)
	} else {
		j, err = s.Jobs.ReorganizeRecord(r.Context(), id)
	}
	if err != nil {
		writeError(w, 400, "reorganize_failed", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "job": j})
}
func (s *Server) cacheEntries(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.Store.CacheEntries(r.Context(), recordFilter(r))
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "config": s.Store.CacheOptions(r.Context())})
}
func (s *Server) deleteCache(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "all" {
		var request struct {
			Confirm bool `json:"confirm"`
		}
		if !decodeJSON(w, r, &request) {
			return
		}
		if !request.Confirm {
			writeError(w, 400, "confirmation_required", "请确认清空缓存")
			return
		}
	}
	if err := s.Store.DeleteCached(r.Context(), id); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "recognition", "清理识别缓存 "+id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) classificationDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, organize.DefaultClassification())
}
func (s *Server) classificationPreview(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Kind   string                        `json:"kind"`
		ID     int64                         `json:"tmdb_id"`
		Config organize.ClassificationConfig `json:"config"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := request.Config.Validate(); err != nil {
		writeError(w, 400, "invalid_rules", err.Error())
		return
	}
	d, err := s.TMDB.Details(r.Context(), request.Kind, request.ID)
	if err != nil {
		writeError(w, 502, "tmdb_failed", err.Error())
		return
	}
	root, sub, rule := request.Config.Match(d)
	writeJSON(w, 200, map[string]any{"title": d.Title, "root": root, "subcategory": sub, "rule": rule, "needs_confirmation": sub == ""})
}
