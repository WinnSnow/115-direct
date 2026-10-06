package organize

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type DirectoryConfig struct {
	InboxCID    string `json:"inbox_cid"`
	LibraryCID  string `json:"library_cid"`
	STRMPath    string `json:"strm_path"`
	PendingPath string `json:"pending_path"`
	GatewayURL  string `json:"gateway_url"`
}

type LibraryReconcileResult struct {
	Created int `json:"created"`
	Moved   int `json:"moved"`
	Skipped int `json:"skipped"`
}

type Service struct {
	Store         *store.Store
	Pan           pan115.Provider
	TMDB          *tmdb.Client
	Secret        []byte
	Defaults      DirectoryConfig
	Refresh       func(context.Context) error
	Notify        func(context.Context, string, string)
	queue         chan string
	once          sync.Once
	operationMu   sync.Mutex
	shareMu       sync.Mutex
	uploadQueueMu sync.Mutex
}

func NewService(st *store.Store, pan pan115.Provider, tmdbClient *tmdb.Client, secret []byte, defaults DirectoryConfig) *Service {
	return &Service{Store: st, Pan: pan, TMDB: tmdbClient, Secret: secret, Defaults: defaults, queue: make(chan string, 128)}
}

func (s *Service) LibraryMutex() *sync.Mutex { return &s.operationMu }

func (s *Service) Start(ctx context.Context) {
	s.once.Do(func() {
		if err := s.migrateClassification(ctx); err != nil {
			slog.Error("classification migration", "error", err)
		}
		if err := s.MigrateLinks(ctx); err != nil {
			slog.Error("association migration", "error", err)
		}
		if err := s.Recover(ctx); err != nil {
			slog.Error("execution recovery", "error", err)
		}
		go s.worker(ctx)
		go s.receivedCleanupLoop(ctx)
		if jobs, err := s.Store.QueuedJobs(ctx); err == nil {
			for _, job := range jobs {
				s.Enqueue(job.ID)
			}
		}
	})
}

func (s *Service) Enqueue(id string) {
	select {
	case s.queue <- id:
	default:
		slog.Warn("organize queue is full", "job", id)
	}
}

func (s *Service) Submit(ctx context.Context, source, sender, shareURL, shareCode, kind string, tmdbID int64) (*store.TransferJob, error) {
	id, err := randomID(12)
	if err != nil {
		return nil, err
	}
	job := &store.TransferJob{ID: id, Source: source, Sender: sender, ShareURL: shareURL, ShareCode: shareCode,
		Status: "queued", TMDBKind: kind, TMDBID: tmdbID}
	job, err = s.Store.CreateShareJob(ctx, job)
	if err != nil {
		return nil, err
	}
	if job.Duplicate {
		s.Store.Audit(ctx, "transfer", "分享已保存，复用任务 "+job.ID)
		return job, nil
	}
	s.Store.Audit(ctx, "transfer", "created job "+id)
	s.Enqueue(id)
	return job, nil
}

func (s *Service) Confirm(ctx context.Context, id, kind string, tmdbID int64) error {
	if kind != "movie" && kind != "tv" {
		return fmt.Errorf("kind must be movie or tv")
	}
	job, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return err
	}
	job.TMDBKind, job.TMDBID, job.Status, job.Error = kind, tmdbID, "received", ""
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		return err
	}
	hint := tmdb.ExtractHint(job.Title)
	_ = s.Store.GetSetting(ctx, "recognition-hint:"+job.ID, &hint)
	if hint.Title != "" {
		raw, _ := json.Marshal(tmdb.Candidate{Kind: kind, ID: tmdbID, Title: job.Title, Reasons: []string{"人工确认"}})
		_ = s.Store.PutCached(ctx, matchCacheKey(hint), "match", hint.Title, raw)
	}
	s.Enqueue(id)
	return nil
}

func (s *Service) Retry(ctx context.Context, id string) error {
	job, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return err
	}
	job.Status, job.Error = "retry", ""
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		return err
	}
	s.Enqueue(id)
	return nil
}

func (s *Service) ReconcileLibrary(ctx context.Context) (*LibraryReconcileResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	if cfg.LibraryCID == "" {
		return nil, fmt.Errorf("整理目录尚未配置")
	}
	result := &LibraryReconcileResult{}
	taxonomy := s.taxonomy(ctx)
	categoryNames := make([]string, 0, len(taxonomy))
	for _, category := range taxonomy {
		categoryNames = append(categoryNames, category.Name)
	}
	categoryIDs, _, created, err := s.ensureNamedDirs(ctx, cfg.LibraryCID, categoryNames)
	if err != nil {
		return nil, err
	}
	result.Created += created
	type categoryState struct {
		name     string
		kind     string
		children []pan115.Entry
	}
	states := make([]categoryState, 0, len(taxonomy))
	subcategoryIDs := make(map[string]map[string]string, len(taxonomy))
	for index, category := range taxonomy {
		ids, children, count, err := s.ensureNamedDirs(ctx, categoryIDs[category.Name], category.Subcategories)
		if err != nil {
			return nil, err
		}
		result.Created += count
		subcategoryIDs[category.Name] = ids
		kind := "movie"
		if index == 1 {
			kind = "tv"
		}
		states = append(states, categoryState{name: category.Name, kind: kind, children: children})
	}

	for _, state := range states {
		known := subcategoryIDs[state.name]
		for _, entry := range state.children {
			if !entry.Directory {
				continue
			}
			if _, ok := known[entry.Name]; ok {
				continue
			}
			hint := tmdb.ExtractHint(entry.Name)
			if hint.TMDBID == 0 {
				result.Skipped++
				continue
			}
			details, err := s.TMDB.Details(ctx, state.kind, hint.TMDBID)
			if err != nil {
				result.Skipped++
				continue
			}
			category, subcategory := s.mediaDirectories(ctx, details)
			targetCID := subcategoryIDs[category][subcategory]
			if targetCID == "" {
				result.Skipped++
				continue
			}
			if err := s.Pan.Move(ctx, targetCID, entry.ID); err != nil {
				return nil, fmt.Errorf("移动 115 目录 %s: %w", entry.Name, err)
			}
			result.Moved++
		}
	}
	s.Store.Audit(ctx, "library", fmt.Sprintf("reconciled taxonomy: created=%d moved=%d skipped=%d", result.Created, result.Moved, result.Skipped))
	return result, nil
}

func (s *Service) ensureNamedDirs(ctx context.Context, parent string, names []string) (map[string]string, []pan115.Entry, int, error) {
	entries, err := s.Pan.List(ctx, parent)
	if err != nil {
		return nil, nil, 0, err
	}
	ids := make(map[string]string, len(names))
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	for _, entry := range entries {
		if entry.Directory && wanted[entry.Name] {
			ids[entry.Name] = entry.ID
		}
	}
	created := 0
	for _, name := range names {
		if ids[name] != "" {
			continue
		}
		id, err := s.Pan.Mkdir(ctx, parent, name)
		if err != nil {
			return nil, nil, created, fmt.Errorf("创建 115 目录 %s: %w", name, err)
		}
		ids[name] = id
		created++
	}
	return ids, entries, created, nil
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.queue:
			if err := s.process(ctx, id); err != nil {
				slog.Error("job failed", "job", id, "error", err)
			}
		}
	}
}

func (s *Service) process(ctx context.Context, id string) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	job, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		job.Status, job.Error = "failed", err.Error()
		_ = s.Store.UpdateJob(context.Background(), job)
		s.notify(job, "整理失败", err.Error())
		return err
	}
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	if job.Source == "local" {
		cfg = s.Directories(ctx)
		return s.processLocal(ctx, cfg, job, fail)
	}
	if job.Source == "manual" {
		return s.processManual(ctx, s.Directories(ctx), job, fail)
	}
	if job.Source == "scrape" {
		job.Status = "organizing"
		_ = s.Store.UpdateJob(ctx, job)
		result, err := s.scrapeLibrary(ctx)
		if err != nil {
			return fail(err)
		}
		if err := s.refreshLibrary(ctx); err != nil {
			return fail(err)
		}
		job.Status, job.Error = "completed", ""
		job.Title = fmt.Sprintf("本地刮削：%d 个条目，%d 个文件", result.Titles, result.Files)
		return s.Store.UpdateJob(ctx, job)
	}
	if cfg.InboxCID == "" || cfg.LibraryCID == "" {
		return fail(fmt.Errorf("接收目录和整理目录尚未配置"))
	}
	// Completed cloud jobs created by older versions may have already produced
	// STRM files and durable media links, but lack an organize plan/execution
	// row.  A retry must reconcile that existing result instead of attempting
	// to receive the share again (the temporary receive directory may already
	// have been cleaned up).  OrganizationRecords projects those links back to
	// the task through MediaLink.InboxID/StageCID.
	if job.Status == "retry" && job.Source != "local" && job.Source != "manual" {
		records, _, recordErr := s.Store.OrganizationRecords(ctx, store.RecordFilter{Search: job.ID, Limit: 500})
		if recordErr != nil {
			return fail(recordErr)
		}
		ready := false
		for _, record := range records {
			if record.RemoteID != "" && record.OutputPath != "" {
				ready = true
				break
			}
		}
		if ready {
			if err := s.refreshLibrary(ctx); err != nil {
				return fail(err)
			}
			job.Status, job.Error, job.CleanupAt = "completed", "", nil
			if err := s.Store.UpdateJob(ctx, job); err != nil {
				return err
			}
			s.Store.Audit(ctx, "organize", "已复核已有 STRM 和 115 组织记录，任务无需重复接收："+job.ID)
			return nil
		}
	}
	var savedPlan OrganizePlan
	if err := s.Store.GetSetting(ctx, "organize_plan:"+job.ID, &savedPlan); err == nil {
		return s.finishModern(ctx, s.Directories(ctx), job, savedPlan.Request, savedPlan.Details, fail)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}

	var selection shareSelection
	if err := s.Store.GetSetting(ctx, "share_selection:"+job.ID, &selection); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	if selection.Snapshot.Code != "" {
		if job.StageCID == "" {
			cid, err := s.ensureDir(ctx, cfg.InboxCID, temporaryStageName(&selection.Snapshot, job.ID))
			if err != nil {
				return fail(err)
			}
			job.StageCID = cid
		}
		job.Status = "transferring"
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return fail(err)
		}
		receiving := selection
		if !selection.Update {
			receiving.Selected = selection.Snapshot.Files
		}
		if err := s.receiveShareUpdate(ctx, job, receiving); err != nil {
			return fail(err)
		}
		entries, err := s.Pan.List(ctx, job.StageCID)
		if err != nil {
			return fail(err)
		}
		actual, _ := json.Marshal(entries)
		job.Actual, job.Status = actual, "received"
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return fail(err)
		}
	} else if job.StageCID == "" {
		job.Status = "transferring"
		_ = s.Store.UpdateJob(ctx, job)
		snapshot := &selection.Snapshot
		if snapshot.Code == "" {
			snapshot, err = s.Pan.SnapshotShare(ctx, job.ShareURL, job.ShareCode)
			if err != nil {
				return fail(err)
			}
		}
		expected, _ := json.Marshal(snapshot.Entries)
		job.Expected, job.Title = expected, snapshot.Title
		stageName := temporaryStageName(snapshot, job.ID)
		stageCID, err := s.Pan.Mkdir(ctx, cfg.InboxCID, stageName)
		if err != nil {
			return fail(err)
		}
		job.StageCID = stageCID
		_ = s.Store.UpdateJob(ctx, job)
		if err := s.Pan.ReceiveShare(ctx, snapshot, job.ShareCode, stageCID); err != nil {
			return fail(err)
		}
		entries, err := s.waitReceived(ctx, stageCID, len(snapshot.Entries))
		if err != nil {
			return fail(err)
		}
		actual, _ := json.Marshal(entries)
		job.Actual, job.Status = actual, "received"
		_ = s.Store.UpdateJob(ctx, job)
	} else {
		stageEntries, listErr := s.Pan.List(ctx, job.StageCID)
		if listErr != nil {
			return fail(listErr)
		}
		if len(stageEntries) == 0 {
			job.Status = "transferring"
			_ = s.Store.UpdateJob(ctx, job)
			snapshot, err := s.Pan.SnapshotShare(ctx, job.ShareURL, job.ShareCode)
			if err != nil {
				return fail(err)
			}
			if err = s.Pan.ReceiveShare(ctx, snapshot, job.ShareCode, job.StageCID); err != nil {
				return fail(err)
			}
			stageEntries, err = s.waitReceived(ctx, job.StageCID, len(snapshot.Entries))
			if err != nil {
				return fail(err)
			}
			actual, _ := json.Marshal(stageEntries)
			job.Actual, job.Status = actual, "received"
			_ = s.Store.UpdateJob(ctx, job)
		}
	}
	entries, err := s.walk(ctx, job.StageCID, "")
	if err != nil {
		return fail(err)
	}
	if selection.Snapshot.Code != "" {
		expected := selection.Snapshot.Files
		if selection.Update {
			expected = selection.Selected
		}
		if err := verifyReceivedManifest(expected, entries); err != nil {
			return fail(err)
		}
	}
	details, err := s.resolve(ctx, job, entries)
	if err != nil {
		return fail(err)
	}
	if details == nil {
		return nil
	}
	job.Status = "organizing"
	job.Title = details.Title
	_ = s.Store.UpdateJob(ctx, job)
	entries, err = s.placeReceived(ctx, cfg, job, details)
	if err != nil {
		return fail(err)
	}
	if c := s.Directories(ctx); c.PendingPath != "" {
		ids, err := s.registerSources(ctx, c, entries, job.StageCID)
		if err != nil {
			return fail(err)
		}
		return s.finishModern(ctx, c, job, OrganizeRequest{IDs: ids, Kind: details.Kind, TMDBID: details.ID}, details, fail)
	}
	media, err := s.organize(ctx, cfg, details, entries)
	if err != nil {
		return fail(err)
	}
	job.Status = "verifying"
	_ = s.Store.UpdateJob(ctx, job)
	if len(media) == 0 {
		return fail(fmt.Errorf("没有可整理的媒体文件"))
	}
	for _, item := range media {
		if err := s.writeSTRM(ctx, cfg, item); err != nil {
			return fail(err)
		}
	}
	if err := s.writeMetadata(ctx, cfg, details, media); err != nil {
		return fail(err)
	}
	if err := s.refreshLibrary(ctx); err != nil {
		return fail(err)
	}
	job.Status, job.Error, job.CleanupAt = "completed", "", nil
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		return err
	}
	s.notify(job, "整理完成", fmt.Sprintf("%s，已生成并刮削 %d 个本地 STRM；115 接收原件保留", details.Title, len(media)))
	return nil
}

func temporaryStageName(snapshot *pan115.ShareSnapshot, jobID string) string {
	name := "接收内容"
	if len(snapshot.Entries) == 1 && snapshot.Entries[0].Directory && strings.TrimSpace(snapshot.Entries[0].Name) != "" {
		name = snapshot.Entries[0].Name
	} else if strings.TrimSpace(snapshot.Title) != "" {
		name = snapshot.Title
	}
	return safeName(name) + " [接收中-" + shortJobID(jobID) + "]"
}

func shortJobID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func isTemporaryStageName(name, jobID string) bool {
	if strings.HasSuffix(name, " [接收中-"+shortJobID(jobID)+"]") {
		return true
	}
	suffix := "-" + jobID
	if !strings.HasSuffix(name, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(name, suffix)
	_, err := time.Parse("20060102-150405", prefix)
	return err == nil
}

func (s *Service) normalizeStage(ctx context.Context, cfg DirectoryConfig, job *store.TransferJob) error {
	if job.StageCID == "" {
		return nil
	}
	inboxEntries, err := s.Pan.List(ctx, cfg.InboxCID)
	if err != nil {
		return err
	}
	var wrapper *pan115.Entry
	for i := range inboxEntries {
		if inboxEntries[i].ID == job.StageCID && isTemporaryStageName(inboxEntries[i].Name, job.ID) {
			wrapper = &inboxEntries[i]
			break
		}
	}
	if wrapper == nil {
		return nil
	}
	entries, err := s.Pan.List(ctx, wrapper.ID)
	if err != nil {
		return err
	}
	if len(entries) == 1 && entries[0].Directory {
		child := entries[0]
		if err := s.Pan.Move(ctx, cfg.InboxCID, child.ID); err != nil {
			return fmt.Errorf("提升接收目录 %s: %w", child.Name, err)
		}
		job.StageCID = child.ID
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return err
		}
		if err := s.Pan.Delete(ctx, wrapper.ID); err != nil {
			s.Store.Audit(ctx, "receive", fmt.Sprintf("orphan staging directory %s: %v", wrapper.ID, err))
		}
		return nil
	}
	name := safeName(job.Title)
	if name == "" {
		name = "接收内容-" + shortJobID(job.ID)
	}
	if wrapper.Name != name {
		if err := s.Pan.Rename(ctx, wrapper.ID, name); err != nil {
			return fmt.Errorf("命名接收目录 %s: %w", name, err)
		}
	}
	return nil
}

func (s *Service) normalizeCompletedStages(ctx context.Context) {
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	if cfg.InboxCID == "" {
		return
	}
	jobs, err := s.Store.ListJobs(ctx, 200)
	if err != nil {
		return
	}
	for i := range jobs {
		job := &jobs[i]
		if job.Status != "completed" || job.StageCID == "" {
			continue
		}
		if err := s.normalizeStage(ctx, cfg, job); err != nil {
			slog.Warn("normalize completed receive directory failed", "job", job.ID, "error", err)
		}
	}
}

func (s *Service) waitReceived(ctx context.Context, cid string, expected int) ([]pan115.Entry, error) {
	delay := time.Second
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		entries, err := s.Pan.List(ctx, cid)
		if err == nil && len(entries) >= expected {
			return entries, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 10*time.Second {
			delay *= 2
		}
	}
	return nil, fmt.Errorf("115 已接受转存，但暂存目录在 2 分钟内未出现预期内容")
}

type sourceFile struct {
	Entry    pan115.Entry
	Relative string
}

func (s *Service) walk(ctx context.Context, cid, prefix string) ([]sourceFile, error) {
	entries, err := s.Pan.List(ctx, cid)
	if err != nil {
		return nil, err
	}
	var out []sourceFile
	for _, entry := range entries {
		rel := filepath.Join(prefix, entry.Name)
		if entry.Directory {
			nested, err := s.walk(ctx, entry.ID, rel)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		} else {
			out = append(out, sourceFile{Entry: entry, Relative: rel})
		}
	}
	return out, nil
}

func (s *Service) resolve(ctx context.Context, job *store.TransferJob, files []sourceFile) (*tmdb.Details, error) {
	if job.TMDBID > 0 {
		var details *tmdb.Details
		var err error
		if job.TMDBKind == "movie" || job.TMDBKind == "tv" {
			details, err = s.TMDB.Details(ctx, job.TMDBKind, job.TMDBID)
		} else {
			details, err = s.detailsByID(ctx, job.TMDBID, "")
		}
		if err != nil {
			return nil, err
		}
		job.TMDBKind, job.TMDBID = details.Kind, details.ID
		_ = s.Store.UpdateJob(ctx, job)
		return details, nil
	}
	job.Status = "matching"
	_ = s.Store.UpdateJob(ctx, job)
	s.Store.Audit(ctx, "recognition", "开始识别任务 "+job.ID+" · "+job.Title)
	largest := ""
	var size int64
	for _, file := range files {
		if classify(file.Entry.Name) == Video && file.Entry.Size > size {
			largest, size = file.Entry.Name, file.Entry.Size
		}
	}
	hint := tmdb.ExtractHint(first(job.Title, largest), largest)
	if job.TMDBKind != "" {
		hint.Kind = job.TMDBKind
	}
	if err := s.Store.PutSetting(ctx, "recognition-hint:"+job.ID, hint); err != nil {
		return nil, err
	}
	if hint.TMDBID > 0 {
		details, err := s.detailsByID(ctx, hint.TMDBID, hint.Kind)
		if err != nil {
			return nil, err
		}
		job.TMDBKind, job.TMDBID, job.Error, job.Candidates = details.Kind, details.ID, "", nil
		_ = s.Store.UpdateJob(ctx, job)
		return details, nil
	}
	if raw, hit, err := s.Store.Cached(ctx, matchCacheKey(hint)); err == nil && hit {
		var match tmdb.Candidate
		if json.Unmarshal(raw, &match) == nil && match.ID > 0 && (match.Kind == "movie" || match.Kind == "tv") {
			details, err := s.TMDB.Details(ctx, match.Kind, match.ID)
			if err == nil {
				job.TMDBKind, job.TMDBID = match.Kind, match.ID
				_ = s.Store.UpdateJob(ctx, job)
				s.Store.Audit(ctx, "recognition", fmt.Sprintf("识别缓存命中 %s → %s/%d；任务 %s", hint.Title, match.Kind, match.ID, job.ID))
				return details, nil
			}
		}
	}
	candidates, err := s.TMDB.Search(ctx, hint.Title)
	if err != nil {
		return nil, err
	}
	candidates = tmdb.Rank(hint, candidates)
	raw, _ := json.Marshal(candidates)
	job.Candidates = raw
	match, ok := tmdb.AutoMatch(candidates)
	if !ok {
		job.Status = "waiting_match"
		job.Error = "TMDB 匹配置信度不足，需要手工指定"
		_ = s.Store.UpdateJob(ctx, job)
		s.notify(job, "需要手工整理", job.Error)
		return nil, nil
	}
	job.TMDBKind, job.TMDBID = match.Kind, match.ID
	_ = s.Store.UpdateJob(ctx, job)
	raw, _ = json.Marshal(match)
	_ = s.Store.PutCached(ctx, matchCacheKey(hint), "match", hint.Title, raw)
	s.Store.Audit(ctx, "recognition", fmt.Sprintf("识别成功 %s → %s/%d；评分 %.1f；依据 %s；任务 %s", hint.Title, match.Kind, match.ID, match.Score, strings.Join(match.Reasons, "；"), job.ID))
	return s.TMDB.Details(ctx, match.Kind, match.ID)
}

func matchCacheKey(h tmdb.Hint) string {
	return store.ContentHash([]byte(fmt.Sprintf("match:%s:%s:%d", strings.ToLower(strings.TrimSpace(h.Title)), h.Kind, h.Year)))
}

func (s *Service) detailsByID(ctx context.Context, id int64, preferredKind string) (*tmdb.Details, error) {
	kinds := []string{"movie", "tv"}
	if preferredKind == "tv" {
		kinds = []string{"tv", "movie"}
	}
	var firstErr error
	for _, kind := range kinds {
		details, err := s.TMDB.Details(ctx, kind, id)
		if err == nil {
			return details, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("TMDB ID %d 既不是电影也不是电视剧: %w", id, firstErr)
}

func (s *Service) organize(ctx context.Context, cfg DirectoryConfig, details *tmdb.Details, files []sourceFile) ([]store.MediaEntry, error) {
	category, subcategory := s.mediaDirectories(ctx, details)
	var output []store.MediaEntry
	ledger, err := s.Store.ListMedia(ctx)
	if err != nil {
		return nil, err
	}
	occupied := map[string]string{}
	for _, item := range ledger {
		occupied[item.STRMPath] = item.RemoteID
	}
	for _, source := range files {
		kind := classify(source.Entry.Name)
		if kind != Video {
			continue
		}
		targetName, relative := "", filepath.Join(category, subcategory, titleFolder(details))
		if details.Kind == "movie" {
			targetName = movieName(details, source.Entry.Name)
		} else {
			ep, ok := parseEpisode(source.Entry.Name)
			if !ok {
				continue
			}
			season := fmt.Sprintf("Season %02d", ep.Season)
			targetName, relative = episodeName(details, ep, source.Entry.Name), filepath.Join(relative, season)
		}
		media, err := s.Store.GetMediaByRemoteID(ctx, source.Entry.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			mediaID, err := randomID(16)
			if err != nil {
				return nil, err
			}
			media = &store.MediaEntry{ID: mediaID, RemoteID: source.Entry.ID}
		}
		localName := strings.TrimSuffix(targetName, filepath.Ext(targetName)) + ".strm"
		path := filepath.Join(first(cfg.STRMPath, s.Defaults.STRMPath), relative, localName)
		if owner := occupied[path]; owner != "" && owner != source.Entry.ID {
			path = filepath.Join(filepath.Dir(path), versionedName(localName, fmt.Sprintf("%x", sha256.Sum256([]byte(source.Entry.ID)))))
		}
		occupied[path] = source.Entry.ID
		media.PickCode, media.SHA1, media.Name = source.Entry.PickCode, source.Entry.SHA1, source.Entry.Name
		media.RemotePath, media.STRMPath = source.Relative, path
		output = append(output, *media)
	}
	return output, nil
}

func (s *Service) ensureDir(ctx context.Context, parent, name string) (string, error) {
	entries, err := s.Pan.List(ctx, parent)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Directory && entry.Name == name {
			return entry.ID, nil
		}
	}
	return s.Pan.Mkdir(ctx, parent, name)
}

func (s *Service) copyOne(ctx context.Context, targetCID, targetName string, source pan115.Entry) (pan115.Entry, error) {
	before, err := s.Pan.List(ctx, targetCID)
	if err != nil {
		return pan115.Entry{}, err
	}
	for _, entry := range before {
		if !entry.Directory && source.SHA1 != "" && entry.SHA1 == source.SHA1 {
			return entry, nil
		}
	}
	for _, entry := range before {
		if !entry.Directory && strings.EqualFold(entry.Name, targetName) && entry.SHA1 != source.SHA1 {
			targetName = versionedName(targetName, source.SHA1)
			break
		}
	}
	seen := map[string]bool{}
	for _, entry := range before {
		seen[entry.ID] = true
	}
	if err := s.Pan.Copy(ctx, targetCID, source.ID); err != nil {
		return pan115.Entry{}, err
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := s.Pan.List(ctx, targetCID)
		if err == nil {
			for _, entry := range entries {
				if !seen[entry.ID] && !entry.Directory && ((source.SHA1 != "" && entry.SHA1 == source.SHA1) || entry.Name == source.Name) {
					if entry.Name != targetName {
						if err := s.Pan.Rename(ctx, entry.ID, targetName); err != nil {
							return pan115.Entry{}, err
						}
						entry.Name = targetName
					}
					return entry, nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return pan115.Entry{}, fmt.Errorf("复制后未在目标目录找到 %s", source.Name)
}

func (s *Service) writeSTRM(ctx context.Context, cfg DirectoryConfig, media store.MediaEntry) error {
	root := cfg.STRMPath
	if root == "" {
		root = s.Defaults.STRMPath
	}
	gateway := strings.TrimRight(cfg.GatewayURL, "/")
	if gateway == "" {
		gateway = strings.TrimRight(s.Defaults.GatewayURL, "/")
	}
	relative := strings.TrimSuffix(media.RemotePath, filepath.Ext(media.RemotePath)) + ".strm"
	path := media.STRMPath
	if path == "" {
		path = filepath.Join(root, relative)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("STRM 路径超出根目录")
	}
	if err := EnsureLocalDirectory(root, filepath.Dir(path)); err != nil {
		return err
	}
	sig := SignMedia(s.Secret, media.ID)
	jellyfinSig := SignJellyfinRequest(s.Secret, media.ID)
	content := fmt.Sprintf("%s/direct/%s?sig=%s&jellyfin_sig=%s\n", gateway, media.ID, sig, jellyfinSig)
	if err := writeAtomic(path, []byte(content)); err != nil {
		return err
	}
	media.STRMPath = path
	previous, previousErr := s.Store.GetMediaByRemoteID(ctx, media.RemoteID)
	if err := s.Store.SetLocalOrganization(ctx, media.RemoteID, rel); err != nil {
		return err
	}
	if err := s.Store.PutMedia(ctx, media); err != nil {
		return err
	}
	if previousErr == nil && previous.STRMPath != "" && previous.STRMPath != path {
		oldRel, err := filepath.Rel(root, previous.STRMPath)
		if err == nil && filepath.IsLocal(oldRel) && validateMetadataDir(root, filepath.Dir(previous.STRMPath)) == nil {
			_ = os.Remove(previous.STRMPath)
			removeGeneratedNFO(strings.TrimSuffix(previous.STRMPath, filepath.Ext(previous.STRMPath)) + ".nfo")
			removeEmptyLocalParents(filepath.Dir(previous.STRMPath), root)
		}
	}
	return nil
}

func (s *Service) notify(job *store.TransferJob, title, body string) {
	if s.Notify != nil && job.Sender != "" {
		s.Notify(context.Background(), job.Sender, title+"\n"+body)
	}
}

func randomID(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func subtitleSuffix(name string) string {
	lang := languageRE.FindString(name)
	if lang != "" {
		return "." + strings.ToLower(lang) + strings.ToLower(filepath.Ext(name))
	}
	return strings.ToLower(filepath.Ext(name))
}

func versionedName(name, sha1 string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	suffix := strings.ToLower(sha1)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if suffix == "" {
		suffix = "alternate"
	}
	return base + " - " + suffix + ext
}
