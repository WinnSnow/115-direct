package store

import (
	"context"
	"database/sql"
	"time"
)

type UploadTask struct {
	ID         string     `json:"id"`
	LocalPath  string     `json:"local_path"`
	TargetCID  string     `json:"target_cid"`
	Filename   string     `json:"filename"`
	Size       int64      `json:"size"`
	SHA1       string     `json:"sha1"`
	PreSHA1    string     `json:"pre_sha1,omitempty"`
	Channel    string     `json:"channel"`
	Status     string     `json:"status"`
	RemoteID   string     `json:"remote_id,omitempty"`
	PickCode   string     `json:"pick_code,omitempty"`
	Rapid      bool       `json:"rapid"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (s *Store) CreateUploadTask(ctx context.Context, task *UploadTask) error {
	now := time.Now().UTC()
	task.CreatedAt, task.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO upload_tasks(id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.LocalPath, task.TargetCID, task.Filename, task.Size, task.SHA1, task.PreSHA1, task.Channel, task.Status, task.RemoteID, task.PickCode, task.Rapid, task.Attempts, task.Error, task.CreatedAt, task.UpdatedAt, task.FinishedAt)
	return err
}

func (s *Store) UpdateUploadTask(ctx context.Context, task *UploadTask) error {
	task.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE upload_tasks SET channel=?,status=?,remote_id=?,pick_code=?,rapid=?,attempts=?,error=?,updated_at=?,finished_at=? WHERE id=?`,
		task.Channel, task.Status, task.RemoteID, task.PickCode, task.Rapid, task.Attempts, Redact(task.Error), task.UpdatedAt, task.FinishedAt, task.ID)
	return err
}

// RebindUploadTask attaches a previously persisted digest task to the current
// local file when its old source was removed before a retry could run.
func (s *Store) RebindUploadTask(ctx context.Context, id, localPath, filename, preSHA1 string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE upload_tasks SET local_path=?,filename=?,pre_sha1=?,updated_at=? WHERE id=?`,
		localPath, filename, preSHA1, time.Now().UTC(), id)
	return err
}

func (s *Store) GetUploadTask(ctx context.Context, id string) (*UploadTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks WHERE id=?`, id)
	return scanUploadTask(row)
}

func (s *Store) FindUploadTask(ctx context.Context, localPath, targetCID, sha1 string, size int64) (*UploadTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks WHERE local_path=? AND target_cid=? AND sha1=? AND size=? ORDER BY created_at DESC LIMIT 1`, localPath, targetCID, sha1, size)
	return scanUploadTask(row)
}

func (s *Store) FindUploadTaskByDigest(ctx context.Context, targetCID, sha1 string, size int64) (*UploadTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks WHERE target_cid=? AND sha1=? AND size=? ORDER BY created_at DESC LIMIT 1`, targetCID, sha1, size)
	return scanUploadTask(row)
}

func (s *Store) ListUploadTasks(ctx context.Context, limit int) ([]UploadTask, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadTask
	for rows.Next() {
		item, err := scanUploadTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) QueuedUploadTasks(ctx context.Context) ([]UploadTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,local_path,target_cid,filename,size,sha1,pre_sha1,channel,status,remote_id,pick_code,rapid,attempts,error,created_at,updated_at,finished_at FROM upload_tasks WHERE status IN ('queued','uploading','retry') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadTask
	for rows.Next() {
		item, err := scanUploadTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

type uploadScanner interface{ Scan(...any) error }

func scanUploadTask(row uploadScanner) (*UploadTask, error) {
	var t UploadTask
	var finished sql.NullTime
	err := row.Scan(&t.ID, &t.LocalPath, &t.TargetCID, &t.Filename, &t.Size, &t.SHA1, &t.PreSHA1, &t.Channel, &t.Status, &t.RemoteID, &t.PickCode, &t.Rapid, &t.Attempts, &t.Error, &t.CreatedAt, &t.UpdatedAt, &finished)
	if finished.Valid {
		t.FinishedAt = &finished.Time
	}
	return &t, err
}
