package pan115

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// UploadResult is the durable part of an upload operation.  The remote id is
// resolved by listing the destination after the OSS callback; this keeps the
// local ledger independent from the particular upload protocol.
type UploadResult struct {
	RemoteID string `json:"remote_id"`
	PickCode string `json:"pick_code,omitempty"`
	SHA1     string `json:"sha1"`
	Size     int64  `json:"size"`
	Rapid    bool   `json:"rapid"`
}

// Uploader is deliberately separate from Provider so existing read-only test
// providers do not need to implement upload operations.
type Uploader interface {
	Upload(context.Context, string, string, string) (*UploadResult, error)
}

// Upload negotiates rapid upload before sending file data, and records which
// path actually succeeded. Mutations share the move/delete serialization lock.
func (p *DriverProvider) Upload(ctx context.Context, localPath, parentID, filename string) (*UploadResult, error) {
	if err := p.beginMutation(ctx); err != nil {
		return nil, err
	}
	defer p.mutationMu.Unlock()
	c, _, err := p.current()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(filename) == "" {
		filename = info.Name()
	}
	h := sha1.New()
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	digest := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	before, err := p.List(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("上传前核验远端文件失败: %w", err)
	}
	for _, entry := range before {
		if !entry.Directory && strings.EqualFold(entry.SHA1, digest) && entry.Size == info.Size() {
			slog.Info("upload reused existing remote file", "filename", filename, "upload_method", "reuse", "uploaded_bytes", 0)
			return &UploadResult{RemoteID: entry.ID, PickCode: entry.PickCode, SHA1: digest, Size: info.Size(), Rapid: true}, nil
		}
	}
	p.mu.RLock()
	httpClient := p.http
	p.mu.RUnlock()
	rapid, err := uploadCookieFile(ctx, c, httpClient, f, parentID, filename, info.Size(), digest)
	if err != nil {
		return nil, fmt.Errorf("115 上传失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := p.List(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("上传后核验远端文件失败: %w", err)
	}
	for _, entry := range after {
		if entry.Directory {
			continue
		}
		if strings.EqualFold(entry.SHA1, digest) && entry.Size == info.Size() {
			return &UploadResult{RemoteID: entry.ID, PickCode: entry.PickCode, SHA1: digest, Size: info.Size(), Rapid: rapid}, nil
		}
	}
	return nil, fmt.Errorf("上传完成但未找到远端文件映射")
}
