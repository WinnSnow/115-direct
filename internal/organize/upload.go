package organize

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

// IngestUploaded registers a file uploaded from the local download directory
// in the normal pending STRM ledger. Recognition and final naming remain the
// existing organize pipeline's responsibility.
func (s *Service) IngestUploaded(ctx context.Context, result *pan115.UploadResult, filename string) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if result == nil || strings.TrimSpace(result.RemoteID) == "" {
		return fmt.Errorf("上传结果缺少远端文件 ID")
	}
	c := s.Directories(ctx)
	if c.PendingPath == "" {
		return fmt.Errorf("待整理目录尚未配置")
	}
	clean := filepath.Base(filename)
	if clean == "." || clean == "" || clean != filename {
		return fmt.Errorf("上传文件名无效")
	}
	relative := filepath.Join("上传", clean)
	strm := filepath.Join(c.PendingPath, strings.TrimSuffix(relative, filepath.Ext(relative))+".strm")
	if err := EnsureLocalDirectory(c.PendingPath, filepath.Dir(strm)); err != nil {
		return err
	}
	id := ""
	if existing, lookupErr := s.Store.GetMediaByRemoteID(ctx, result.RemoteID); lookupErr == nil {
		id = existing.ID
	}
	if id == "" {
		var err error
		id, err = randomID(16)
		if err != nil {
			return err
		}
	}
	m := store.MediaEntry{ID: id, RemoteID: result.RemoteID, PickCode: result.PickCode, SHA1: result.SHA1, Name: clean, RemotePath: relative, STRMPath: strm, UpdatedAt: time.Now().UTC()}
	if err := writeAtomic(strm, []byte(s.mediaContent(c, m))); err != nil {
		return err
	}
	link := store.MediaLink{RemoteID: result.RemoteID, SourcePath: strm, IngestedAt: time.Now().UTC(), Mode: "upload", Kind: "unknown"}
	if err := s.Store.PutLink(ctx, link); err != nil {
		return err
	}
	if err := s.Store.PutMedia(ctx, m); err != nil {
		return err
	}
	s.Store.Audit(ctx, "upload", "已生成待整理 STRM："+clean)
	return s.queueUploadedLocal(ctx, m)
}
