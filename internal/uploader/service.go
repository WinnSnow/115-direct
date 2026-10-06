package uploader

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type Config struct {
	Enabled       bool   `json:"enabled"`
	LocalDir      string `json:"local_dir"`
	TargetCID     string `json:"target_cid"`
	Channel       string `json:"channel"` // auto, open_oauth, cookie
	ScanInterval  int    `json:"scan_interval_seconds"`
	StableSeconds int    `json:"stable_seconds"`
}

type Service struct {
	Store      *store.Store
	Pan        pan115.Provider
	Cfg        Config
	queue      chan string
	once       sync.Once
	mu         sync.Mutex
	cfgMu      sync.RWMutex
	seen       map[string]fileState
	OnUploaded func(context.Context, *pan115.UploadResult, string) error
	OpenHTTP   *http.Client
}

func (s *Service) Config() Config { s.cfgMu.RLock(); defer s.cfgMu.RUnlock(); return s.Cfg }
func (s *Service) Configure(cfg Config) {
	if cfg.ScanInterval < 1 {
		cfg.ScanInterval = 30
	}
	if cfg.StableSeconds < 1 {
		cfg.StableSeconds = 30
	}
	s.cfgMu.Lock()
	s.Cfg = cfg
	s.cfgMu.Unlock()
}

type fileState struct {
	size   int64
	mod    time.Time
	stable int
}

func New(st *store.Store, pan pan115.Provider, cfg Config) *Service {
	if cfg.ScanInterval < 1 {
		cfg.ScanInterval = 30
	}
	if cfg.StableSeconds < 1 {
		cfg.StableSeconds = 30
	}
	return &Service{Store: st, Pan: pan, Cfg: cfg, queue: make(chan string, 256), seen: map[string]fileState{}}
}

func (s *Service) Start(ctx context.Context) {
	s.once.Do(func() {
		if tasks, err := s.Store.QueuedUploadTasks(ctx); err == nil {
			for _, task := range tasks {
				s.enqueue(task.ID)
			}
		}
		go s.worker(ctx)
		go s.scanLoop(ctx)
		go s.watchLoop(ctx)
	})
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.queue:
			if err := s.run(ctx, id); err != nil {
				slog.Error("upload task failed", "task", id, "error", err)
			}
		}
	}
}

func (s *Service) enqueue(id string) {
	select {
	case s.queue <- id:
	default:
	}
}
func (s *Service) Enqueue(id string) { s.enqueue(id) }

func (s *Service) scanLoop(ctx context.Context) {
	cfg := s.Config()
	ticker := time.NewTicker(time.Duration(cfg.ScanInterval) * time.Second)
	defer ticker.Stop()
	_ = s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.scan(ctx)
		}
	}
}

// watchLoop is an event accelerator; the periodic scan remains the recovery
// mechanism for missed events and for files created while the service is down.
func (s *Service) watchLoop(ctx context.Context) {
	cfg := s.Config()
	if !cfg.Enabled || cfg.LocalDir == "" {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("upload watcher unavailable", "error", err)
		return
	}
	defer w.Close()
	_ = filepath.Walk(cfg.LocalDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			_ = w.Add(path)
		}
		return nil
	})
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					_ = w.Add(event.Name)
				}
				_ = s.scan(ctx)
			}
		case err, ok := <-w.Errors:
			if ok && err != nil {
				slog.Warn("upload watcher error", "error", err)
			}
		}
	}
}

func (s *Service) scan(ctx context.Context) error {
	cfg := s.Config()
	if !cfg.Enabled || strings.TrimSpace(cfg.LocalDir) == "" || strings.TrimSpace(cfg.TargetCID) == "" {
		return nil
	}
	return filepath.Walk(cfg.LocalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() || ignored(path) {
			return nil
		}
		s.mu.Lock()
		prev, ok := s.seen[path]
		state := fileState{size: info.Size(), mod: info.ModTime()}
		if ok && prev.size == state.size && prev.mod.Equal(state.mod) {
			state.stable = prev.stable + 1
		}
		s.seen[path] = state
		s.mu.Unlock()
		if state.stable == 0 || time.Since(info.ModTime()) < time.Duration(cfg.StableSeconds)*time.Second {
			return nil
		}
		task, err := s.prepare(ctx, path, info, cfg)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("upload scan failed", "path", path, "error", err)
		}
		if task != nil && task.Status == "queued" {
			s.enqueue(task.ID)
		}
		return nil
	})
}

func ignored(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".part" || ext == ".tmp" || ext == ".crdownload" || ext == ".download" || strings.HasPrefix(filepath.Base(path), ".")
}

func (s *Service) prepare(ctx context.Context, path string, info os.FileInfo, cfg Config) (*store.UploadTask, error) {
	h, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	return s.EnqueueCompletedFile(ctx, path, info.Name(), info.Size(), h.sha1, h.pre, cfg.TargetCID, cfg.Channel)
}

// EnqueueCompletedFile registers a file that is already complete on disk. It
// is used by the browser upload finalizer and by the directory scanner. The
// supplied digests avoid another full read for resumable browser uploads.
func (s *Service) EnqueueCompletedFile(ctx context.Context, path, filename string, size int64, digest, preDigest, targetCID, channel string) (*store.UploadTask, error) {
	if targetCID == "" {
		return nil, errors.New("upload target directory is not configured")
	}
	if channel == "" {
		channel = "auto"
	}
	if existing, err := s.Store.FindUploadTaskByDigest(ctx, targetCID, digest, size); err == nil {
		if _, statErr := os.Stat(existing.LocalPath); errors.Is(statErr, os.ErrNotExist) {
			if err := s.Store.RebindUploadTask(ctx, existing.ID, path, filename, preDigest); err != nil {
				return nil, err
			}
			existing.LocalPath = path
			existing.Filename = filename
			existing.PreSHA1 = preDigest
		}
		return existing, nil
	} else if !store.IsMissingSetting(err) && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if existing, err := s.Store.FindUploadTask(ctx, path, targetCID, digest, size); err == nil {
		if existing.Status == "completed" || existing.Status == "uploading" {
			return existing, nil
		}
		return existing, nil
	} else if !store.IsMissingSetting(err) && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	idBytes := sha256.Sum256([]byte(path + "\x00" + targetCID + "\x00" + digest))
	task := &store.UploadTask{ID: hex.EncodeToString(idBytes[:12]), LocalPath: path, TargetCID: targetCID, Filename: filename, Size: size, SHA1: digest, PreSHA1: preDigest, Channel: channel, Status: "queued"}
	if err := s.Store.CreateUploadTask(ctx, task); err != nil {
		return nil, err
	}
	s.Store.Audit(ctx, "upload", "本地文件已加入上传队列："+filepath.Base(path))
	return task, nil
}

type fileHash struct{ sha1, pre string }

func hashFile(path string) (fileHash, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileHash{}, err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return fileHash{}, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fileHash{}, err
	}
	buf := make([]byte, 128*1024)
	n, _ := io.ReadFull(f, buf)
	p := sha1.Sum(buf[:n])
	return fileHash{sha1: strings.ToUpper(hex.EncodeToString(h.Sum(nil))), pre: strings.ToUpper(hex.EncodeToString(p[:]))}, nil
}

func (s *Service) run(ctx context.Context, id string) error {
	task, err := s.Store.GetUploadTask(ctx, id)
	if err != nil {
		return err
	}
	if task.Status == "completed" {
		return nil
	}
	task.Status, task.Attempts, task.Error = "uploading", task.Attempts+1, ""
	if err := s.Store.UpdateUploadTask(ctx, task); err != nil {
		return err
	}
	result, channel, err := s.upload(ctx, task)
	if err != nil {
		task.Status, task.Error, task.Channel = "retry", err.Error(), channel
		_ = s.Store.UpdateUploadTask(ctx, task)
		return err
	}
	now := time.Now().UTC()
	task.Status, task.RemoteID, task.PickCode, task.Rapid, task.Channel, task.FinishedAt = "completed", result.RemoteID, result.PickCode, result.Rapid, channel, &now
	if err := s.Store.UpdateUploadTask(ctx, task); err != nil {
		return err
	}
	if s.OnUploaded != nil {
		if err := s.OnUploaded(ctx, result, task.Filename); err != nil {
			task.Status, task.Error = "retry", "上传已完成但本地映射失败: "+err.Error()
			_ = s.Store.UpdateUploadTask(ctx, task)
			return err
		}
	}
	s.Store.Audit(ctx, "upload", fmt.Sprintf("上传完成：%s（通道 %s，%s）", task.Filename, channel, rapidLabel(result.Rapid)))
	return nil
}

func rapidLabel(v bool) string {
	if v {
		return "秒传"
	}
	return "普通上传"
}

func (s *Service) upload(ctx context.Context, task *store.UploadTask) (*pan115.UploadResult, string, error) {
	channel := s.Config().Channel
	if channel == "" {
		channel = "auto"
	}
	var open pan115.OpenCredentials
	_ = s.Store.GetSetting(ctx, "115_open_oauth", &open)
	if open.AccessToken != "" && open.RefreshToken != "" && open.ExpiresAt > 0 && time.Until(time.Unix(open.ExpiresAt, 0)) < 2*time.Minute {
		if token, err := (&pan115.OpenAuthClient{HTTP: s.OpenHTTP}).Refresh(ctx, open.ClientID, open.RefreshToken); err == nil {
			open.AccessToken, open.RefreshToken = token.AccessToken, token.RefreshToken
			if token.ExpiresIn > 0 {
				open.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
			}
			_ = s.Store.PutSetting(ctx, "115_open_oauth", open)
		} else {
			slog.Warn("Open OAuth refresh failed; Cookie channel may be used", "error", err)
		}
	}
	if (channel == "auto" || channel == "open_oauth") && open.AccessToken != "" {
		u := &pan115.OpenUploader{Creds: open, HTTP: s.OpenHTTP}
		result, err := u.Upload(ctx, task.LocalPath, task.TargetCID, task.Filename)
		if err == nil {
			return result, "open_oauth", nil
		}
		if channel == "open_oauth" || !errors.Is(err, pan115.ErrOpenUploadRequiresDataChannel) {
			return nil, "open_oauth", err
		}
	}
	u, ok := s.Pan.(pan115.Uploader)
	if !ok {
		return nil, "cookie", fmt.Errorf("当前 115 驱动不支持 Cookie 上传")
	}
	result, err := u.Upload(ctx, task.LocalPath, task.TargetCID, task.Filename)
	return result, "cookie", err
}
