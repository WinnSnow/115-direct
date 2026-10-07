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
	projection := s.addCloudOrganizationPaths(r.Context(), job, items)
	message := "确认 TMDB 条目后生成待整理和成品路径"
	if len(items) > 0 {
		message = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":     job.ID,
		"status":     job.Status,
		"title":      job.Title,
		"items":      items,
		"mode":       mode,
		"available":  len(items) > 0,
		"message":    message,
		"projection": projection,
	})
}

// cloudProjection describes the state of the two paths shown by the transfer
// page. A completed task can have a durable organization record while one of
// the 115 paths is temporarily unavailable, so the UI must not collapse every
// empty field into "no organization record".
type cloudProjection struct {
	RecordCount  int    `json:"record_count"`
	SourceCount  int    `json:"source_count"`
	TargetCount  int    `json:"target_count"`
	SourceReady  bool   `json:"source_ready"`
	TargetReady  bool   `json:"target_ready"`
	SourceReason string `json:"source_reason,omitempty"`
	TargetReason string `json:"target_reason,omitempty"`
	Retryable    bool   `json:"retryable"`
}

// addCloudOrganizationPaths projects the two 115 paths needed by the transfer
// page. Local filesystem paths are deliberately left to the media organization
// page. A copied file receives a new 115 ID, so source lookup uses the stable
// file name (and the direct ID when the source was not copied) rather than
// assuming the target ID is also the source ID.
func (s *Server) addCloudOrganizationPaths(ctx context.Context, job *store.TransferJob, items []store.OrganizationRecord) cloudProjection {
	projection := cloudProjection{RecordCount: len(items)}
	if job == nil {
		projection.SourceReason = "任务不存在，无法读取 115 源目录"
		projection.TargetReason = "任务不存在，无法读取 115 媒体库目录"
		return projection
	}
	if len(items) == 0 {
		projection.SourceReason = "任务已完成，但没有找到对应的 115 组织记录"
		projection.TargetReason = projection.SourceReason
		projection.Retryable = job.Status != "failed"
		return projection
	}
	if s.Jobs == nil {
		projection.SourceReason = "整理服务未初始化，无法读取 115 源目录"
		projection.TargetReason = "整理服务未初始化，无法读取 115 媒体库目录"
		projection.Retryable = true
		return projection
	}
	cfg := s.Jobs.Directories(ctx)
	if cfg.InboxCID == "" || cfg.LibraryCID == "" {
		projection.SourceReason = "115 接收目录或整理目录尚未配置"
		projection.TargetReason = projection.SourceReason
		return projection
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
		if root == "" {
			root = cfg.InboxCID
		}
		if root != "" && root != items[i].RemoteID {
			groups[root] = append(groups[root], i)
		}
	}
	sources := map[string]string{}
	for root, indexes := range groups {
		if s.Pan == nil {
			projection.Retryable = true
			break
		}
		wantedNames := map[string]bool{}
		wantedIDs := map[string]bool{}
		wantedSHA1 := map[string]bool{}
		sha1ByRecord := map[string]string{}
		for _, index := range indexes {
			if strings.TrimSpace(items[index].Name) != "" {
				wantedNames[items[index].Name] = true
			}
			if strings.TrimSpace(items[index].RemoteID) != "" {
				wantedIDs[items[index].RemoteID] = true
			}
			if media, err := s.Store.GetMediaByRemoteID(ctx, items[index].RemoteID); err == nil && strings.TrimSpace(media.SHA1) != "" {
				wantedSHA1[media.SHA1] = true
				sha1ByRecord[items[index].ID] = media.SHA1
			}
		}
		byID, byName, bySHA1 := map[string]string{}, map[string]string{}, map[string]string{}
		// The record normally carries InboxID. Older automatic runs may only
		// have the task StageCID, and manual records may have neither. In both
		// cases scan the configured receive root as a final recovery path.
		roots := []string{}
		if root != "" {
			roots = append(roots, root)
		}
		if cfg.InboxCID != "" && cfg.InboxCID != root {
			roots = append(roots, cfg.InboxCID)
		}
		for _, candidate := range roots {
			names := cloneStringSet(wantedNames)
			ids := cloneStringSet(wantedIDs)
			sha1s := cloneStringSet(wantedSHA1)
			idsFound, namesFound, sha1Found := map[string]string{}, map[string]string{}, map[string]string{}
			if err := collectCloudFiles(ctx, s.Pan, candidate, "", names, ids, sha1s, idsFound, namesFound, sha1Found, map[string]bool{}); err != nil {
				projection.Retryable = true
				continue
			}
			for key, value := range idsFound {
				byID[key] = value
			}
			for key, value := range namesFound {
				byName[key] = value
			}
			for key, value := range sha1Found {
				bySHA1[key] = value
			}
			matched := 0
			for _, index := range indexes {
				if byID[items[index].RemoteID] != "" || byName[items[index].Name] != "" || bySHA1[sha1ByRecord[items[index].ID]] != "" {
					matched++
				}
			}
			if matched == len(indexes) {
				break
			}
		}
		for _, index := range indexes {
			relative := byID[items[index].RemoteID]
			if relative == "" {
				relative = byName[items[index].Name]
			}
			if relative == "" {
				relative = bySHA1[sha1ByRecord[items[index].ID]]
			}
			if relative != "" {
				sources[items[index].ID] = relative
			}
		}
	}

	for i := range items {
		item := &items[i]
		kind := item.Kind
		if kind == "" {
			kind = job.TMDBKind
		}
		relative := sources[item.ID]
		if relative != "" {
			root := item.InboxID
			if root == "" {
				root = job.StageCID
			}
			itemStageName := stageName
			if s.Pan != nil {
				itemStageName = cloudStageName(ctx, s.Pan, cfg.InboxCID, root)
			}
			parts := []string{inboxRoot}
			if kind == "tv" && itemStageName != "" {
				parts = append(parts, cloudTVSourceFolder(itemStageName, relative))
			} else if kind == "tv" {
				parts = append(parts, firstCloudPathPart(relative))
			} else if itemStageName != "" {
				parts = append(parts, itemStageName)
				parts = append(parts, relative)
			} else {
				// Legacy/manual records may have no InboxID. When the fallback
				// scan starts at the configured receive root, the first relative
				// component is the receive work folder.
				first := firstCloudPathPart(relative)
				rest := strings.TrimPrefix(relative, first)
				rest = strings.TrimPrefix(rest, "/")
				if rest != "" {
					parts = append(parts, first, rest)
				} else {
					parts = append(parts, relative)
				}
			}
			item.CloudSourcePath = cloudJoin(parts...)
		}
		targetRelative := item.CloudTargetPath
		if targetRelative == "" && item.OutputPath != "" && cfg.STRMPath != "" {
			if rel, err := filepath.Rel(cfg.STRMPath, item.OutputPath); err == nil && filepath.IsLocal(rel) && rel != "." {
				targetRelative = filepath.ToSlash(rel)
			}
		}
		if targetRelative != "" {
			if kind == "tv" {
				targetRelative = cloudTVFolder(targetRelative)
			}
			item.CloudTargetPath = cloudJoin(libraryRoot, targetRelative)
		}
		if item.CloudSourcePath != "" || item.CloudTargetPath != "" {
			item.CloudOperation = cloudOperation(job.Source)
		}
		if item.CloudSourcePath != "" {
			projection.SourceCount++
		}
		if item.CloudTargetPath != "" {
			projection.TargetCount++
		}
	}
	projection.SourceReady = projection.SourceCount == projection.RecordCount
	projection.TargetReady = projection.TargetCount == projection.RecordCount
	if !projection.SourceReady {
		projection.SourceReason = "已找到整理记录，但未在 115 接收目录中找到对应源文件；可能是接收目录已清理、移动或文件名发生变化"
		projection.Retryable = true
	}
	if !projection.TargetReady {
		projection.TargetReason = "已找到整理记录，但没有得到 115 媒体库目标路径"
		projection.Retryable = true
	}
	return projection
}

func cloneStringSet(values map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func firstCloudPathPart(value string) string {
	parts := strings.Split(strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func cloudTVSourceFolder(stageName, relative string) string {
	base := strings.TrimSpace(strings.SplitN(stageName, " [接收中-", 2)[0])
	if strings.Contains(stageName, " [接收中-") {
		if first := firstCloudPathPart(relative); first != "" && !strings.HasPrefix(strings.ToLower(first), "season ") {
			return first
		}
	}
	if base != "" {
		return base
	}
	return firstCloudPathPart(relative)
}

// TV cloud paths are displayed at the work-folder level. Episode files and
// Season directories are implementation details of the receive/copy tree and
// make the transfer pipeline unnecessarily noisy.
func cloudTVFolder(value string) string {
	parts := strings.Split(strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/"), "/")
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, "/")
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

func collectCloudFiles(ctx context.Context, provider pan115.Provider, cid, prefix string, wantedNames, wantedIDs, wantedSHA1 map[string]bool, byID, byName, bySHA1 map[string]string, seen map[string]bool) error {
	if cid == "" || (len(wantedNames) == 0 && len(wantedIDs) == 0 && len(wantedSHA1) == 0) || seen[cid] {
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
			if err := collectCloudFiles(ctx, provider, entry.ID, relative, wantedNames, wantedIDs, wantedSHA1, byID, byName, bySHA1, seen); err != nil {
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
		if entry.SHA1 != "" && wantedSHA1[entry.SHA1] {
			bySHA1[entry.SHA1] = relative
			delete(wantedSHA1, entry.SHA1)
		}
		if len(wantedNames) == 0 && len(wantedIDs) == 0 && len(wantedSHA1) == 0 {
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
