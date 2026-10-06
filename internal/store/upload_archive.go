package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Store) UploadedSource(ctx context.Context, remoteID string) (*UploadTask, error) {
	return scanUploadTask(s.db.QueryRowContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks WHERE remote_id=? AND status='completed' ORDER BY created_at DESC LIMIT 1`, remoteID))
}

// Transfer the unpublished playback identity to its verified classification
// copy. The upload receipt continues to reference the retained reception file.
func (s *Store) AdoptUploadedCopy(ctx context.Context, originalID string, m MediaEntry, l MediaLink) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM media_entries WHERE remote_id=?`, originalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM media_entries WHERE remote_id=?`, m.RemoteID).Scan(&id); err != nil {
			return err
		}
		if id != m.ID {
			return fmt.Errorf("分类副本已有其他播放关联")
		}
		return nil
	} else if err != nil {
		return err
	}
	if id != m.ID || originalID == m.RemoteID || l.RemoteID != m.RemoteID {
		return fmt.Errorf("上传分类关联无效")
	}
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM media_links WHERE remote_id=?`, originalID).Scan(&body); err != nil {
		return err
	}
	var old MediaLink
	if err := json.Unmarshal(body, &old); err != nil {
		return err
	}
	if old.Mode != "upload" || old.OutputPath != "" || old.Deleted != "" {
		return fmt.Errorf("上传源已有成品或关联已变化，请重新预览")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_entries SET remote_id=?,pick_code=?,sha1=?,name=?,remote_path=?,strm_path=?,missing_count=0,updated_at=? WHERE remote_id=?`, m.RemoteID, m.PickCode, m.SHA1, m.Name, m.RemotePath, m.STRMPath, time.Now().UTC(), originalID); err != nil {
		return err
	}
	body, err = json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_links(remote_id,body) VALUES(?,?)`, l.RemoteID, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_links WHERE remote_id=?`, originalID); err != nil {
		return err
	}
	return tx.Commit()
}
