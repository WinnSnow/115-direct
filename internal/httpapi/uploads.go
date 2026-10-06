package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/uploader"
)

const (
	tusVersion       = "1.0.0"
	maxUploadChunk   = int64(64 << 20)
	uploadSessionTTL = 7 * 24 * time.Hour
)

var uploadSessionLocks sync.Map

func (s *Server) uploads(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListUploadTasks(r.Context(), 200)
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) retryUpload(w http.ResponseWriter, r *http.Request) {
	task, err := s.Store.GetUploadTask(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "upload_not_found", "上传任务不存在")
		return
	}
	task.Status, task.Error = "queued", ""
	if err := s.Store.UpdateUploadTask(r.Context(), task); err != nil {
		writeInternal(w, err)
		return
	}
	if s.Upload != nil {
		s.Upload.Enqueue(task.ID)
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) uploadSettings(w http.ResponseWriter, r *http.Request) {
	cfg := uploader.Config{LocalDir: "", Channel: "auto", StableSeconds: 30}
	if s.Upload != nil {
		cfg = s.Upload.Config()
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) putUploadSettings(w http.ResponseWriter, r *http.Request) {
	var cfg uploader.Config
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if cfg.Channel != "" && cfg.Channel != "auto" && cfg.Channel != "open_oauth" && cfg.Channel != "cookie" {
		writeError(w, http.StatusBadRequest, "invalid_channel", "上传通道无效")
		return
	}
	if cfg.LocalDir == "" || !filepath.IsAbs(cfg.LocalDir) {
		writeError(w, http.StatusBadRequest, "invalid_upload_dir", "本地上传目录必须是绝对路径")
		return
	}
	if info, err := os.Stat(cfg.LocalDir); err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "invalid_upload_dir", "本地上传目录不存在")
		return
	}
	if cfg.StableSeconds < 1 {
		cfg.StableSeconds = 30
	}
	if cfg.ScanInterval < 1 {
		cfg.ScanInterval = 30
	}
	if err := s.Store.PutSetting(r.Context(), "upload", cfg); err != nil {
		writeInternal(w, err)
		return
	}
	if s.Upload != nil {
		s.Upload.Configure(cfg)
	}
	s.Store.Audit(r.Context(), "upload", "上传监控设置已更新")
	writeJSON(w, http.StatusOK, cfg)
}

type createUploadSessionRequest struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	TargetCID string `json:"target_cid"`
	Channel   string `json:"channel"`
}

func setTUSHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) uploadSessions(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.Auth.CurrentUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	s.cleanupExpiredUploadSessions(r)
	items, err := s.Store.ListUploadSessions(r.Context(), identity.Username, identity.Role == "admin", 200)
	if err != nil {
		writeInternal(w, err)
		return
	}
	for i := range items {
		s.refreshUploadSessionFromTask(r, &items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createUploadSession(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.Auth.CurrentUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	cfg := uploader.Config{Channel: "auto"}
	if s.Upload != nil {
		cfg = s.Upload.Config()
	}
	if !cfg.Enabled {
		writeError(w, http.StatusConflict, "upload_disabled", "本地上传服务未启用")
		return
	}
	var req createUploadSessionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := safeUploadFilename(req.Filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filename", err.Error())
		return
	}
	if req.Size <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_size", "文件大小必须大于零")
		return
	}
	targetCID := strings.TrimSpace(req.TargetCID)
	if targetCID == "" {
		targetCID = strings.TrimSpace(cfg.TargetCID)
	}
	if targetCID == "" {
		writeError(w, http.StatusBadRequest, "upload_target_missing", "请先配置 115 接收目录")
		return
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		channel = cfg.Channel
	}
	if channel == "" {
		channel = "auto"
	}
	if channel != "auto" && channel != "open_oauth" && channel != "cookie" {
		writeError(w, http.StatusBadRequest, "invalid_channel", "上传通道无效")
		return
	}
	if cfg.LocalDir == "" || !filepath.IsAbs(cfg.LocalDir) {
		writeError(w, http.StatusBadRequest, "invalid_upload_dir", "本地上传目录必须是绝对路径")
		return
	}
	if err := os.MkdirAll(filepath.Join(cfg.LocalDir, ".incoming"), 0o750); err != nil {
		writeInternal(w, err)
		return
	}
	if !hasUploadFreeSpace(cfg.LocalDir, req.Size) {
		writeError(w, http.StatusInsufficientStorage, "upload_disk_full", "上传目录剩余空间不足")
		return
	}
	id, err := randomUploadID()
	if err != nil {
		writeInternal(w, err)
		return
	}
	now := time.Now().UTC()
	session := &store.UploadSession{
		ID: id, OwnerUsername: identity.Username, Filename: req.Filename, SafeFilename: name,
		TotalSize: req.Size, TargetCID: targetCID, Channel: channel,
		TempPath: filepath.Join(cfg.LocalDir, ".incoming", id+".part"), Status: "created",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(uploadSessionTTL),
	}
	if req.SHA256 != "" {
		session.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
		if len(session.SHA256) != sha256.Size*2 {
			writeError(w, http.StatusBadRequest, "invalid_sha256", "SHA-256 格式无效")
			return
		}
		if _, err := hex.DecodeString(session.SHA256); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_sha256", "SHA-256 格式无效")
			return
		}
	}
	if err := s.Store.CreateUploadSession(r.Context(), session); err != nil {
		writeInternal(w, err)
		return
	}
	setTUSHeaders(w)
	w.Header().Set("Location", "/api/v1/upload/sessions/"+id)
	w.Header().Set("Upload-Offset", "0")
	w.Header().Set("Upload-Length", fmt.Sprintf("%d", session.TotalSize))
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) uploadSessionHead(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	setTUSHeaders(w)
	w.Header().Set("Upload-Offset", fmt.Sprintf("%d", session.ReceivedSize))
	w.Header().Set("Upload-Length", fmt.Sprintf("%d", session.TotalSize))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) uploadSessionPatch(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	setTUSHeaders(w)
	if session.Status != "created" && session.Status != "receiving" {
		w.Header().Set("Upload-Offset", fmt.Sprintf("%d", session.ReceivedSize))
		writeError(w, http.StatusConflict, "upload_not_receiving", "上传会话当前状态不能写入")
		return
	}
	offset, err := parseUploadOffset(r.Header.Get("Upload-Offset"))
	if err != nil || offset != session.ReceivedSize {
		w.Header().Set("Upload-Offset", fmt.Sprintf("%d", session.ReceivedSize))
		writeError(w, http.StatusConflict, "upload_offset_mismatch", "上传偏移已变化，请重新获取进度")
		return
	}
	length := r.ContentLength
	if length < 0 {
		length, err = parseUploadOffset(r.Header.Get("Upload-Chunk-Length"))
	}
	if length <= 0 || length > maxUploadChunk || offset+length > session.TotalSize {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_chunk", "分片大小无效")
		return
	}
	unlock := lockUploadSession(session.ID)
	defer unlock()
	// Reload after taking the lock so a previous request in this process is
	// reflected before touching the file.
	session, ok = s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	if offset != session.ReceivedSize {
		w.Header().Set("Upload-Offset", fmt.Sprintf("%d", session.ReceivedSize))
		writeError(w, http.StatusConflict, "upload_offset_mismatch", "上传偏移已变化，请重新获取进度")
		return
	}
	if err := os.MkdirAll(filepath.Dir(session.TempPath), 0o750); err != nil {
		writeInternal(w, err)
		return
	}
	f, err := os.OpenFile(session.TempPath, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		writeInternal(w, err)
		return
	}
	defer f.Close()
	if info, statErr := f.Stat(); statErr == nil && info.Size() != offset {
		if info.Size() > offset {
			if err := f.Truncate(offset); err != nil {
				writeInternal(w, err)
				return
			}
		} else {
			w.Header().Set("Upload-Offset", fmt.Sprintf("%d", info.Size()))
			writeError(w, http.StatusConflict, "upload_file_offset_mismatch", "临时文件与上传记录不一致")
			return
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		writeInternal(w, err)
		return
	}
	chunkHash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(f, chunkHash), r.Body, length)
	if err != nil || n != length {
		_ = f.Truncate(offset)
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		writeError(w, http.StatusBadRequest, "chunk_incomplete", err.Error())
		return
	}
	if err := verifyTUSChecksum(r.Header.Get("Upload-Checksum"), chunkHash.Sum(nil)); err != nil {
		_ = f.Truncate(offset)
		w.Header().Set("Upload-Offset", fmt.Sprintf("%d", offset))
		writeError(w, 460, "chunk_checksum_failed", err.Error())
		return
	}
	if err := f.Sync(); err != nil {
		writeInternal(w, err)
		return
	}
	advanced, err := s.Store.AdvanceUploadSession(r.Context(), session.ID, offset, offset+length)
	if err != nil {
		writeInternal(w, err)
		return
	}
	if !advanced {
		latest, _ := s.Store.GetUploadSession(r.Context(), session.ID)
		if latest != nil {
			w.Header().Set("Upload-Offset", fmt.Sprintf("%d", latest.ReceivedSize))
		}
		writeError(w, http.StatusConflict, "upload_offset_mismatch", "上传偏移已变化，请重新获取进度")
		return
	}
	w.Header().Set("Upload-Offset", fmt.Sprintf("%d", offset+length))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadSessionGet(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	s.refreshUploadSessionFromTask(r, session)
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) uploadSessionComplete(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	unlock := lockUploadSession(session.ID)
	defer unlock()
	session, ok = s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	if session.Status == "completed" || session.Status == "queued" || session.Status == "remote_uploading" {
		writeJSON(w, http.StatusOK, session)
		return
	}
	if session.ReceivedSize != session.TotalSize {
		writeError(w, http.StatusConflict, "upload_incomplete", "文件尚未上传完成")
		return
	}
	session.Status, session.Error = "verifying", ""
	if err := s.Store.UpdateUploadSession(r.Context(), session); err != nil {
		writeInternal(w, err)
		return
	}
	sourcePath := session.TempPath
	if session.FinalPath != "" {
		sourcePath = session.FinalPath
	}
	hash, err := hashCompletedUpload(sourcePath)
	if err != nil {
		session.Status, session.Error = "failed", "读取临时文件失败："+err.Error()
		_ = s.Store.UpdateUploadSession(r.Context(), session)
		writeError(w, http.StatusInternalServerError, "upload_verify_failed", session.Error)
		return
	}
	if session.SHA256 != "" && !strings.EqualFold(session.SHA256, hash.sha256) {
		session.Status, session.Error = "failed", "SHA-256 校验失败"
		_ = s.Store.UpdateUploadSession(r.Context(), session)
		writeError(w, http.StatusUnprocessableEntity, "upload_checksum_failed", session.Error)
		return
	}
	session.SHA256, session.SHA1, session.PreSHA1 = hash.sha256, hash.sha1, hash.pre
	finalPath := session.FinalPath
	if finalPath == "" {
		root := filepath.Dir(filepath.Dir(session.TempPath))
		finalPath, err = allocateUploadPath(root, session.SafeFilename)
		if err != nil {
			session.Status, session.Error = "failed", "创建目标文件失败："+err.Error()
			_ = s.Store.UpdateUploadSession(r.Context(), session)
			writeInternal(w, err)
			return
		}
		if err := os.Rename(session.TempPath, finalPath); err != nil {
			session.Status, session.Error = "failed", "写入正式文件失败："+err.Error()
			_ = s.Store.UpdateUploadSession(r.Context(), session)
			writeInternal(w, err)
			return
		}
		session.FinalPath = finalPath
		// Persist the renamed path before any subsequent hashing, queueing, or
		// process-visible state changes. A restart after os.Rename must retain
		// the only durable reference to the formal file.
		if err := s.Store.UpdateUploadSession(context.WithoutCancel(r.Context()), session); err != nil {
			writeInternal(w, err)
			return
		}
	}
	if s.Upload == nil {
		session.Status, session.Error = "ready", "上传服务未启动"
		_ = s.Store.UpdateUploadSession(r.Context(), session)
		writeError(w, http.StatusServiceUnavailable, "upload_service_unavailable", session.Error)
		return
	}
	task, err := s.Upload.EnqueueCompletedFile(r.Context(), finalPath, session.SafeFilename, session.TotalSize, session.SHA1, session.PreSHA1, session.TargetCID, session.Channel)
	if err != nil {
		session.Status, session.Error = "ready", "加入 115 队列失败："+err.Error()
		_ = s.Store.UpdateUploadSession(r.Context(), session)
		writeError(w, http.StatusInternalServerError, "upload_queue_failed", session.Error)
		return
	}
	if task.LocalPath != finalPath {
		_ = os.Remove(finalPath)
		session.FinalPath = task.LocalPath
	}
	session.UploadTaskID, session.Status, session.Error = task.ID, "queued", ""
	if task.Status == "queued" || task.Status == "retry" {
		s.Upload.Enqueue(task.ID)
	}
	if err := s.Store.UpdateUploadSession(r.Context(), session); err != nil {
		writeInternal(w, err)
		return
	}
	s.Store.Audit(r.Context(), "upload", "浏览器文件已完成本地接收："+session.SafeFilename)
	s.refreshUploadSessionFromTask(r, session)
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) uploadSessionDelete(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedUploadSession(w, r)
	if !ok {
		return
	}
	if session.Status == "completed" || session.Status == "remote_uploading" || session.Status == "queued" {
		writeError(w, http.StatusConflict, "upload_already_started", "远端上传已经开始")
		return
	}
	s.removeUploadSessionFiles(r.Context(), session)
	session.Status, session.Error = "cancelled", "用户取消上传"
	if err := s.Store.UpdateUploadSession(r.Context(), session); err != nil {
		writeInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RecoverUploadSessions makes a session durable across a process restart that
// happened after local finalization but before the remote task was queued.
// Such sessions are returned to the resumable "ready" state; the normal
// complete endpoint will hash the retained formal file and enqueue it.
func (s *Server) RecoverUploadSessions(ctx context.Context) error {
	items, err := s.Store.ListUploadSessions(ctx, "", true, 500)
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].Status != "verifying" {
			continue
		}
		if items[i].FinalPath != "" {
			if _, statErr := os.Stat(items[i].FinalPath); statErr == nil {
				items[i].Status, items[i].Error = "ready", ""
				if err := s.Store.UpdateUploadSession(ctx, &items[i]); err != nil {
					return err
				}
				continue
			}
		}
		// If finalization did not persist a usable formal file, leave the
		// temporary upload resumable when it still exists. Otherwise expose a
		// durable failure instead of an unrecoverable verifying state.
		if _, statErr := os.Stat(items[i].TempPath); statErr == nil {
			items[i].Status, items[i].Error = "receiving", ""
		} else {
			items[i].Status, items[i].Error = "failed", "上传校验状态无法恢复，请重新上传"
		}
		if err := s.Store.UpdateUploadSession(ctx, &items[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) authorizedUploadSession(w http.ResponseWriter, r *http.Request) (*store.UploadSession, bool) {
	identity, ok := s.Auth.CurrentUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return nil, false
	}
	session, err := s.Store.GetUploadSession(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "upload_session_not_found", "上传会话不存在")
		return nil, false
	}
	if session.OwnerUsername != identity.Username && identity.Role != "admin" {
		writeError(w, http.StatusForbidden, "upload_session_forbidden", "没有权限访问该上传会话")
		return nil, false
	}
	return session, true
}

func (s *Server) refreshUploadSessionFromTask(r *http.Request, session *store.UploadSession) {
	if session.UploadTaskID == "" {
		return
	}
	task, err := s.Store.GetUploadTask(r.Context(), session.UploadTaskID)
	if err != nil {
		return
	}
	switch task.Status {
	case "completed":
		session.Status, session.Error = "completed", ""
	case "uploading":
		session.Status, session.Error = "remote_uploading", ""
	case "retry":
		session.Status, session.Error = "failed", task.Error
	}
}

func (s *Server) cleanupExpiredUploadSessions(r *http.Request) {
	items, err := s.Store.ExpiredUploadSessions(r.Context(), time.Now().UTC(), 50)
	if err != nil {
		return
	}
	for i := range items {
		s.removeUploadSessionFiles(r.Context(), &items[i])
		items[i].Status = "expired"
		items[i].Error = "上传会话已过期"
		_ = s.Store.UpdateUploadSession(r.Context(), &items[i])
	}
}

func (s *Server) removeUploadSessionFiles(ctx context.Context, session *store.UploadSession) {
	ctx = context.WithoutCancel(ctx)
	_ = os.Remove(session.TempPath)
	if session.FinalPath == "" || session.FinalPath == session.TempPath {
		return
	}
	// A retrying remote task still owns the finalized source file. Keep it
	// available for the uploader worker and let task completion clean it up.
	if session.UploadTaskID != "" {
		if _, err := s.Store.GetUploadTask(ctx, session.UploadTaskID); err == nil {
			return
		}
	}
	_ = os.Remove(session.FinalPath)
}

func lockUploadSession(id string) func() {
	value, _ := uploadSessionLocks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func randomUploadID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func safeUploadFilename(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "" || strings.ContainsRune(name, 0) {
		return "", errors.New("文件名无效")
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "", errors.New("文件名无效")
	}
	return name, nil
}

func parseUploadOffset(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, errors.New("缺少 Upload-Offset")
	}
	var offset int64
	_, err := fmt.Sscan(value, &offset)
	if err != nil || offset < 0 {
		return 0, errors.New("Upload-Offset 无效")
	}
	return offset, nil
}

func verifyTUSChecksum(value string, digest []byte) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return errors.New("仅支持 sha256 分片校验")
	}
	want, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || !strings.EqualFold(hex.EncodeToString(want), hex.EncodeToString(digest)) {
		return errors.New("分片校验失败")
	}
	return nil
}

type completedUploadHash struct{ sha256, sha1, pre string }

func hashCompletedUpload(path string) (completedUploadHash, error) {
	f, err := os.Open(path)
	if err != nil {
		return completedUploadHash{}, err
	}
	defer f.Close()
	h256, h1 := sha256.New(), sha1.New()
	buf := make([]byte, 1024*1024)
	prefix := make([]byte, 0, 128*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = h256.Write(buf[:n])
			_, _ = h1.Write(buf[:n])
			if len(prefix) < 128*1024 {
				need := 128*1024 - len(prefix)
				if need > n {
					need = n
				}
				prefix = append(prefix, buf[:need]...)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return completedUploadHash{}, readErr
		}
	}
	pre := sha1.Sum(prefix)
	return completedUploadHash{sha256: hex.EncodeToString(h256.Sum(nil)), sha1: strings.ToUpper(hex.EncodeToString(h1.Sum(nil))), pre: strings.ToUpper(hex.EncodeToString(pre[:]))}, nil
}

func allocateUploadPath(root, name string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("上传目录无效")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i <= 10000; i++ {
		candidate := filepath.Join(root, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("同名文件过多")
}

func hasUploadFreeSpace(path string, required int64) bool {
	if required < 0 {
		return false
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return true
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	// Keep a small reserve so a verification pass and SQLite WAL can finish
	// even when the uploaded file nearly fills the volume.
	reserve := uint64(256 << 20)
	return uint64(required) <= available && available-uint64(required) >= reserve
}
