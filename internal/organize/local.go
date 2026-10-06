package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func (s *Service) ReconcileLocalLibrary(ctx context.Context) (*LibraryReconcileResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	if root == "" {
		return nil, fmt.Errorf("STRM 目录尚未配置")
	}
	result := &LibraryReconcileResult{}
	for _, category := range s.taxonomy(ctx) {
		for _, subcategory := range category.Subcategories {
			path := filepath.Join(root, category.Name, subcategory)
			_, err := os.Stat(path)
			if os.IsNotExist(err) {
				result.Created++
			} else if err != nil {
				return nil, err
			}
			if err := EnsureLocalDirectory(root, path); err != nil {
				return nil, err
			}
			if err := validateMetadataDir(root, path); err != nil {
				return nil, err
			}
		}
	}
	s.Store.Audit(ctx, "library", fmt.Sprintf("本地分类目录已校准，新建 %d；115 未修改", result.Created))
	return result, nil
}

func (s *Service) SubmitLocal(ctx context.Context, relative, kind string, tmdbID int64) (*store.TransferJob, error) {
	if kind != "" && kind != "movie" && kind != "tv" {
		return nil, fmt.Errorf("媒体类型无效")
	}
	if tmdbID < 0 {
		return nil, fmt.Errorf("TMDB ID 无效")
	}
	clean := filepath.Clean(relative)
	if !filepath.IsLocal(clean) || clean == "." {
		return nil, fmt.Errorf("请选择一部电影或电视剧的本地目录")
	}
	base := filepath.Base(clean)
	for _, category := range libraryTaxonomy {
		if base == category.Name {
			return nil, fmt.Errorf("请选择单部作品目录，不要选择分类目录")
		}
		for _, sub := range category.Subcategories {
			if base == sub {
				return nil, fmt.Errorf("请选择单部作品目录，不要选择分类目录")
			}
		}
	}
	if base == "待整理" {
		return nil, fmt.Errorf("请选择待整理目录下的单部作品")
	}
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	path := filepath.Join(root, clean)
	if pending := first(cfg.PendingPath, s.Defaults.PendingPath); pending != "" {
		root = pending
		path = filepath.Join(root, clean)
	}
	if err := validateMetadataDir(root, path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("本地目录不存在")
	}
	id, err := randomID(12)
	if err != nil {
		return nil, err
	}
	job := &store.TransferJob{ID: id, Source: "local", StageCID: filepath.ToSlash(clean), Title: filepath.Base(clean), Status: "queued", TMDBKind: kind, TMDBID: tmdbID}
	if err := s.Store.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	s.Store.Audit(ctx, "scrape", "本地整理任务已排队："+job.Title)
	s.Enqueue(id)
	return job, nil
}

func (s *Service) SubmitScrape(ctx context.Context) (*store.TransferJob, error) {
	id, err := randomID(12)
	if err != nil {
		return nil, err
	}
	job := &store.TransferJob{ID: id, Source: "scrape", Title: "本地媒体库刮削", Status: "queued"}
	if err := s.Store.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	s.Enqueue(id)
	return job, nil
}

func (s *Service) processLocal(ctx context.Context, cfg DirectoryConfig, job *store.TransferJob, fail func(error) error) error {
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	if cfg.PendingPath != "" {
		root = cfg.PendingPath
		var saved OrganizePlan
		if err := s.Store.GetSetting(ctx, "organize_plan:"+job.ID, &saved); err == nil {
			return s.finishModern(ctx, cfg, job, saved.Request, saved.Details, fail)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fail(err)
		}
	}
	path := filepath.Join(root, job.StageCID)
	if !filepath.IsLocal(job.StageCID) {
		return fail(fmt.Errorf("本地目录无效"))
	}
	var selectedIDs []string
	if len(job.Expected) > 0 {
		if err := json.Unmarshal(job.Expected, &selectedIDs); err != nil {
			return fail(err)
		}
	}
	selected := map[string]bool{}
	for _, id := range selectedIDs {
		selected[id] = true
	}
	if len(selected) == 0 {
		if err := validateMetadataDir(root, path); err != nil {
			return fail(err)
		}
	}
	ledger, err := s.Store.ListMedia(ctx)
	if err != nil {
		return fail(err)
	}
	var files []sourceFile
	for _, media := range ledger {
		sourcePath := media.STRMPath
		if cfg.PendingPath != "" {
			l, err := s.Store.MediaLink(ctx, media.RemoteID)
			if err != nil {
				return fail(err)
			}
			if l.Deleted != "" || l.OutputPath != "" {
				continue
			}
			sourcePath = l.SourcePath
		}
		rel, err := filepath.Rel(path, sourcePath)
		if len(selected) > 0 && !selected[media.RemoteID] {
			continue
		}
		if len(selected) == 0 && (err != nil || !filepath.IsLocal(rel)) {
			continue
		}
		if _, err := os.Stat(sourcePath); err != nil {
			continue
		}
		if err := validateMetadataDir(root, filepath.Dir(sourcePath)); err != nil {
			return fail(err)
		}
		files = append(files, sourceFile{Entry: pan115.Entry{ID: media.RemoteID, Name: media.Name, PickCode: media.PickCode, SHA1: media.SHA1, Size: 1}, Relative: media.RemotePath})
		if len(selected) == 0 {
			selectedIDs = append(selectedIDs, media.RemoteID)
		}
	}
	if len(files) == 0 {
		return fail(fmt.Errorf("目录中没有已同步的 STRM 文件"))
	}
	if len(selected) == 0 {
		job.Expected, _ = json.Marshal(selectedIDs)
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return fail(err)
		}
	}
	details, err := s.resolve(ctx, job, files)
	if err != nil {
		return fail(err)
	}
	if details == nil {
		return nil
	}
	job.Title, job.Status = details.Title, "organizing"
	_ = s.Store.UpdateJob(ctx, job)
	if cfg.PendingPath != "" {
		return s.finishModern(ctx, cfg, job, OrganizeRequest{IDs: selectedIDs, Kind: details.Kind, TMDBID: details.ID}, details, fail)
	}
	items, err := s.organize(ctx, cfg, details, files)
	if err != nil {
		return fail(err)
	}
	if len(items) == 0 {
		return fail(fmt.Errorf("没有可整理文件；电视剧文件需要 SxxExx 集号"))
	}
	for _, item := range items {
		if err := s.writeSTRM(ctx, cfg, item); err != nil {
			return fail(err)
		}
	}
	if err := s.writeMetadata(ctx, cfg, details, items); err != nil {
		return fail(err)
	}
	if err := s.refreshLibrary(ctx); err != nil {
		return fail(err)
	}
	job.Status, job.Error, job.CleanupAt = "completed", "", nil
	s.Store.Audit(ctx, "scrape", fmt.Sprintf("本地分类整理完成 %s，%d 个 STRM；115 未修改", strings.TrimSpace(details.Title), len(items)))
	return s.Store.UpdateJob(ctx, job)
}

type LocalOrganizeResult struct {
	Mode     string `json:"mode"`
	Queued   int    `json:"queued"`
	Requeued int    `json:"requeued"`
	Skipped  int    `json:"skipped"`
}

func (s *Service) QueuePendingLocal(ctx context.Context) error {
	_, err := s.QueuePendingLocalMode(ctx, false)
	return err
}

// QueuePendingLocalFull runs a complete local organization pass. New pending
// directories are queued as usual, while already published local links are
// requeued through ReorganizeRecord so current naming and placement rules are
// reconciled by the existing idempotent organizer.
func (s *Service) QueuePendingLocalFull(ctx context.Context) (*LocalOrganizeResult, error) {
	return s.QueuePendingLocalMode(ctx, true)
}

func (s *Service) QueuePendingLocalMode(ctx context.Context, full bool) (*LocalOrganizeResult, error) {
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	if pending := first(cfg.PendingPath, s.Defaults.PendingPath); pending != "" {
		root = pending
	}
	ledger, err := s.Store.ListMedia(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := s.Store.ListJobs(ctx, 200)
	if err != nil {
		return nil, err
	}
	pending := map[string]bool{}
	for _, job := range jobs {
		if job.Source == "local" && job.Status != "completed" && job.Status != "cleaned" {
			pending[job.StageCID] = true
		}
	}
	paths := map[string]bool{}
	for _, item := range ledger {
		if l, err := s.Store.MediaLink(ctx, item.RemoteID); err == nil && l.Mode == "upload" {
			if l.Deleted == "" && !l.Unavailable && l.Suppressed == "" && l.OutputPath == "" {
				if err := s.queueUploadedLocal(ctx, item); err != nil {
					return nil, err
				}
			}
			continue
		}
		if first(cfg.PendingPath, s.Defaults.PendingPath) != "" {
			l, err := s.Store.MediaLink(ctx, item.RemoteID)
			if err != nil {
				return nil, err
			}
			if l.Deleted != "" || l.OutputPath != "" || l.Unavailable || l.Suppressed != "" {
				continue
			}
			item.STRMPath = l.SourcePath
		}
		if _, err := s.Store.LocalOrganization(ctx, item.RemoteID); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		rel, err := filepath.Rel(root, item.STRMPath)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 || (first(cfg.PendingPath, s.Defaults.PendingPath) == "" && parts[0] != "待整理" && tmdb.ExtractHint(filepath.Dir(rel)).TMDBID == 0) {
			continue
		}
		dir := filepath.Dir(rel)
		if strings.HasPrefix(strings.ToLower(filepath.Base(dir)), "season ") {
			dir = filepath.Dir(dir)
		}
		if !pending[filepath.ToSlash(dir)] {
			paths[dir] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if _, err := s.SubmitLocal(ctx, path, "", 0); err != nil {
			return nil, err
		}
	}
	result := &LocalOrganizeResult{Mode: "incremental", Queued: len(ordered)}
	if !full {
		return result, nil
	}
	result.Mode = "full"
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return nil, err
	}
	pendingRoot := first(cfg.PendingPath, s.Defaults.PendingPath)
	strmRoot := first(cfg.STRMPath, s.Defaults.STRMPath)
	for _, link := range links {
		if link.RemoteID == "" || link.OutputPath == "" || link.Deleted != "" || link.Unavailable || link.Suppressed != "" || link.Kind == "" || link.TMDBID <= 0 {
			continue
		}
		if (pendingRoot == "" || !within(pendingRoot, link.SourcePath)) && (strmRoot == "" || !within(strmRoot, link.SourcePath)) {
			continue
		}
		job, err := s.ReorganizeRecord(ctx, "media:"+link.RemoteID)
		if err != nil {
			result.Skipped++
			continue
		}
		if job != nil {
			result.Requeued++
		}
	}
	return result, nil
}
