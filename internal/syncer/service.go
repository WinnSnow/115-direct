package syncer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type Config struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
}

type Status struct {
	Running     bool      `json:"running"`
	RecordID    string    `json:"record_id,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	LastRun     time.Time `json:"last_run,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Files       int       `json:"files"`
	Created     int       `json:"created"`
	Removed     int       `json:"removed"`
	Unavailable int       `json:"unavailable"`
}

type Service struct {
	Store     *store.Store
	Pan       pan115.Provider
	Secret    []byte
	Defaults  organize.DirectoryConfig
	Refresh   func(context.Context) error
	OnChanged func(context.Context) error
	LibraryMu *sync.Mutex
	mu        sync.Mutex
	status    Status
}

func New(st *store.Store, pan pan115.Provider, secret []byte, defaults organize.DirectoryConfig) *Service {
	return &Service{Store: st, Pan: pan, Secret: secret, Defaults: defaults}
}

func (s *Service) Status() Status { s.mu.Lock(); defer s.mu.Unlock(); return s.status }

func (s *Service) Start(ctx context.Context) {
	if err := s.Store.RecoverSyncRecords(ctx); err != nil {
		slog.Error("sync record recovery", "error", err)
	}
	go func() {
		nextFull := time.Now().Add(24 * time.Hour)
		for {
			cfg := Config{Enabled: true, IntervalMinutes: 5}
			_ = s.Store.GetSetting(ctx, "sync", &cfg)
			if cfg.IntervalMinutes < 1 {
				cfg.IntervalMinutes = 5
			}
			if cfg.IntervalMinutes > 1440 {
				cfg.IntervalMinutes = 1440
			}
			wait := time.Duration(cfg.IntervalMinutes) * time.Minute
			if untilFull := time.Until(nextFull); cfg.Enabled && untilFull < wait {
				wait = untilFull
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if cfg.Enabled {
				var err error
				if !time.Now().Before(nextFull) {
					err = s.run(ctx, true, "scheduled", false)
					nextFull = time.Now().Add(24 * time.Hour)
				} else {
					err = s.run(ctx, false, "scheduled", false)
				}
				if err != nil {
					slog.Warn("scheduled sync failed", "error", err)
				}
			}
		}
	}()
}

func (s *Service) RunIncremental(ctx context.Context) error {
	return s.run(ctx, false, "manual", false)
}

func (s *Service) RunFull(ctx context.Context) error {
	return s.run(ctx, true, "manual", false)
}

func (s *Service) Queue(full bool) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("同步任务正在运行")
	}
	s.status.Running = true
	s.mu.Unlock()
	go func() {
		if err := s.run(context.Background(), full, "manual", true); err != nil {
			slog.Warn("manual sync failed", "error", err)
		}
	}()
	return nil
}

func (s *Service) run(ctx context.Context, reconcileMissing bool, trigger string, reserved bool) (result error) {
	s.mu.Lock()
	if s.status.Running && !reserved {
		s.mu.Unlock()
		return fmt.Errorf("同步任务正在运行")
	}
	s.status.Running, s.status.LastError = true, ""
	s.mu.Unlock()
	mode := "incremental"
	if reconcileMissing {
		mode = "full"
	}
	status := Status{Running: false, LastRun: time.Now().UTC(), RecordID: fmt.Sprintf("sync-%d", time.Now().UnixNano()), Mode: mode}
	s.mu.Lock()
	s.status = status
	s.status.Running = true
	s.mu.Unlock()
	record := store.SyncRecord{ID: status.RecordID, Mode: mode, Trigger: trigger, Status: "running", StartedAt: status.LastRun}
	defer func() {
		if result != nil {
			status.LastError = result.Error()
		}
		finished := time.Now().UTC()
		record.FinishedAt = &finished
		record.Status = "completed"
		record.Files = status.Files
		record.Created = status.Created
		record.Unavailable = status.Unavailable
		record.Error = status.LastError
		if status.LastError != "" {
			record.Status = "failed"
		}
		if err := s.Store.PutSyncRecord(context.Background(), record); err != nil {
			slog.Error("sync record write", "error", err)
		}
		s.Store.Audit(context.Background(), "sync", fmt.Sprintf("%s同步%s · 扫描%d个视频 · 新增%d条映射 · 源不可用%d条 · 任务%s · %s", mode, record.Status, status.Files, status.Created, status.Unavailable, record.ID, record.Error))
		s.mu.Lock()
		s.status = status
		s.mu.Unlock()
	}()
	if s.LibraryMu != nil {
		s.LibraryMu.Lock()
		defer s.LibraryMu.Unlock()
	}
	var dirs organize.DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &dirs); err != nil {
		dirs = s.Defaults
	}
	record.LibraryCID = dirs.LibraryCID
	if err := s.Store.PutSyncRecord(ctx, record); err != nil {
		return err
	}
	s.Store.Audit(ctx, "sync", fmt.Sprintf("开始%s同步 · 目录CID %s · 触发%s · 任务%s", mode, dirs.LibraryCID, trigger, record.ID))
	if dirs.LibraryCID == "" {
		status.LastError = "整理目录尚未配置"
		return errors.New(status.LastError)
	}
	if dirs.STRMPath == "" {
		dirs.STRMPath = s.Defaults.STRMPath
	}
	if dirs.GatewayURL == "" {
		dirs.GatewayURL = s.Defaults.GatewayURL
	}
	if dirs.PendingPath == "" {
		dirs.PendingPath = s.Defaults.PendingPath
	}
	if dirs.PendingPath != "" {
		if err := organize.CheckRoots(ctx, s.Store, dirs); err != nil {
			status.LastError = err.Error()
			return err
		}
	}
	seen := map[string]bool{}
	changed := false
	err := s.walk(ctx, dirs, dirs.LibraryCID, "", seen, &status, &changed)
	if err != nil {
		status.LastError = err.Error()
		return err
	}
	if reconcileMissing {
		ledger, err := s.Store.ListMedia(ctx)
		if err != nil {
			status.LastError = err.Error()
			return err
		}
		for _, entry := range ledger {
			if seen[entry.RemoteID] {
				continue
			}
			// Upload-origin media is staged outside the cloud library tree and
			// can legitimately have a different target CID. A full library
			// walk must not mark those pending uploads as missing.
			if link, linkErr := s.Store.MediaLink(ctx, entry.RemoteID); linkErr == nil && link.Mode == "upload" {
				continue
			}
			missing, err := s.Store.IncrementMediaMissing(ctx, entry.RemoteID)
			if err != nil {
				continue
			}
			if missing >= 1 {
				link, err := s.Store.MediaLink(ctx, entry.RemoteID)
				if errors.Is(err, sql.ErrNoRows) {
					link = &store.MediaLink{RemoteID: entry.RemoteID, OutputPath: entry.STRMPath, IngestedAt: entry.UpdatedAt}
				} else if err != nil {
					return err
				}
				link.Unavailable = true
				if err := s.Store.PutLink(ctx, *link); err != nil {
					return err
				}
				if link.Deleted != "" {
					continue
				}
				status.Unavailable++
				if err := s.Store.QueueReview(ctx, "cloud:"+entry.RemoteID, entry.RemoteID, "云端源缺失；保留成品和关联，等待确认删除或移出目录"); err != nil {
					return err
				}
			}
		}
	}
	if changed && s.OnChanged != nil {
		if err := s.OnChanged(ctx); err != nil {
			status.LastError = err.Error()
			return err
		}
	}
	if s.Refresh != nil {
		if err := s.Refresh(ctx); err != nil {
			s.Store.Log(ctx, "jellyfin", "error", "同步完成后的媒体库更新通知失败："+err.Error())
			status.LastError = err.Error()
			return err
		}
		s.Store.Audit(ctx, "jellyfin", "本地 STRM 同步完成，已通知 Jellyfin 更新媒体库；整理成品完成后再次通知")
	}
	return nil
}

func (s *Service) walk(ctx context.Context, dirs organize.DirectoryConfig, cid, prefix string, seen map[string]bool, status *Status, changed *bool) error {
	entries, err := s.Pan.List(ctx, cid)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := safeComponent(entry.Name)
		rel := filepath.Join(prefix, name)
		if entry.Directory {
			if err := s.walk(ctx, dirs, entry.ID, rel, seen, status, changed); err != nil {
				return err
			}
			continue
		}
		if !isVideo(entry.Name) {
			continue
		}
		if seen[entry.ID] {
			continue
		}
		status.Files++
		seen[entry.ID] = true
		media, err := s.Store.GetMediaByRemoteID(ctx, entry.ID)
		if err != nil && !isNotFound(err) {
			return err
		}
		if media == nil || isNotFound(err) {
			id := mediaID(entry.ID)
			media = &store.MediaEntry{ID: id, RemoteID: entry.ID}
			status.Created++
			*changed = true
		}
		media.PickCode, media.SHA1, media.Name, media.RemotePath = entry.PickCode, entry.SHA1, name, rel
		if dirs.PendingPath != "" {
			if err := s.syncPending(ctx, dirs, media, rel, changed); err != nil {
				return err
			}
			continue
		}
		strmRel := strings.TrimSuffix(rel, filepath.Ext(rel)) + ".strm"
		if tmdb.ExtractHint(filepath.Dir(rel)).TMDBID == 0 {
			strmRel = filepath.Join("待整理", strmRel)
		}
		organized, err := s.Store.LocalOrganization(ctx, entry.ID)
		if err == nil {
			if !filepath.IsLocal(organized) {
				return fmt.Errorf("本地整理路径无效")
			}
			strmRel = organized
		} else if !isNotFound(err) {
			return err
		}
		strmPath := filepath.Join(dirs.STRMPath, strmRel)
		previousPath := media.STRMPath
		if err := organize.EnsureLocalDirectory(dirs.STRMPath, filepath.Dir(strmPath)); err != nil {
			return err
		}
		content := strings.TrimRight(dirs.GatewayURL, "/") + "/direct/" + media.ID + "?sig=" + organize.SignMedia(s.Secret, media.ID) + "&jellyfin_sig=" + organize.SignJellyfinRequest(s.Secret, media.ID) + "\n"
		existing, _ := os.ReadFile(strmPath)
		if string(existing) != content {
			if err := writeAtomic(strmPath, []byte(content)); err != nil {
				return err
			}
			*changed = true
		}
		media.STRMPath = strmPath
		if err := s.Store.PutMedia(ctx, *media); err != nil {
			return err
		}
		if previousPath != "" && previousPath != strmPath {
			_ = os.Remove(previousPath)
			removeEmptyParents(filepath.Dir(previousPath), dirs.STRMPath)
		}
	}
	return nil
}

func (s *Service) syncPending(ctx context.Context, dirs organize.DirectoryConfig, media *store.MediaEntry, rel string, changed *bool) error {
	link, err := s.Store.MediaLink(ctx, media.RemoteID)
	if errors.Is(err, sql.ErrNoRows) {
		link = &store.MediaLink{RemoteID: media.RemoteID, IngestedAt: time.Now().UTC()}
		if media.STRMPath != "" {
			link.IngestedAt = media.UpdatedAt
			link.OutputPath = media.STRMPath
			link.Manual = true
		}
	} else if err != nil {
		return err
	}
	link.Unavailable = false
	if err := s.Store.ResolveReview(ctx, "cloud:"+media.RemoteID, "resolved"); err != nil {
		return err
	}
	if link.Deleted != "" {
		return s.Store.PutLink(ctx, *link)
	}
	if link.SourcePath == "" {
		link.SourcePath = filepath.Join(dirs.PendingPath, strings.TrimSuffix(rel, filepath.Ext(rel))+".strm")
	}
	if !withinRoot(dirs.PendingPath, link.SourcePath) {
		return fmt.Errorf("待整理映射超出根目录")
	}
	if link.Mode != "move" || link.OutputPath == "" {
		if err := organize.EnsureLocalDirectory(dirs.PendingPath, filepath.Dir(link.SourcePath)); err != nil {
			return err
		}
		content := strings.TrimRight(dirs.GatewayURL, "/") + "/direct/" + media.ID + "?sig=" + organize.SignMedia(s.Secret, media.ID) + "&jellyfin_sig=" + organize.SignJellyfinRequest(s.Secret, media.ID) + "\n"
		if info, err := os.Lstat(link.SourcePath); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("待整理文件不是普通文件")
		}
		existing, readErr := os.ReadFile(link.SourcePath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		if string(existing) != content {
			if readErr == nil {
				return fmt.Errorf("待整理文件内容已被修改，需人工确认")
			}
			if err := writeAtomic(link.SourcePath, []byte(content)); err != nil {
				return err
			}
			*changed = true
		}
	}
	if link.OutputPath != "" {
		if _, err := os.Lstat(link.OutputPath); os.IsNotExist(err) {
			if err := s.Store.QueueReview(ctx, "local:"+media.RemoteID, media.RemoteID, "成品缺失，可能删除或移出目录；不自动重建或删除云端"); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if err := s.Store.ResolveReview(ctx, "local:"+media.RemoteID, "resolved"); err != nil {
				return err
			}
		}
	}
	media.STRMPath = link.OutputPath
	if media.STRMPath == "" {
		media.STRMPath = link.SourcePath
	}
	if err := s.Store.PutLink(ctx, *link); err != nil {
		return err
	}
	return s.Store.PutMedia(ctx, *media)
}
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

func removeEmptyParents(path, root string) {
	root = filepath.Clean(root)
	for path = filepath.Clean(path); path != root && strings.HasPrefix(path, root+string(filepath.Separator)); path = filepath.Dir(path) {
		if err := os.Remove(path); err != nil {
			return
		}
	}
}

func mediaID(remote string) string {
	sum := sha256.Sum256([]byte("115-direct|" + remote))
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}
func safeComponent(name string) string {
	name = strings.NewReplacer("/", "_", "\\", "_").Replace(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}
func isVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".ts", ".m2ts", ".mov", ".wmv", ".webm", ".iso":
		return true
	}
	return false
}
func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no rows") || err == sql.ErrNoRows
}
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
