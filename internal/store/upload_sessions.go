package store

import (
	"context"
	"time"
)

// UploadSession is the durable browser-to-service upload state. It is kept
// separate from UploadTask because a file can finish arriving locally while
// the later 115 upload is still queued or retrying.
type UploadSession struct {
	ID            string    `json:"id"`
	OwnerUsername string    `json:"owner_username"`
	Filename      string    `json:"filename"`
	SafeFilename  string    `json:"safe_filename"`
	TotalSize     int64     `json:"total_size"`
	ReceivedSize  int64     `json:"received_size"`
	SHA256        string    `json:"sha256,omitempty"`
	SHA1          string    `json:"sha1,omitempty"`
	PreSHA1       string    `json:"pre_sha1,omitempty"`
	TargetCID     string    `json:"target_cid"`
	Channel       string    `json:"channel"`
	TempPath      string    `json:"-"`
	FinalPath     string    `json:"final_path,omitempty"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	UploadTaskID  string    `json:"upload_task_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *Store) CreateUploadSession(ctx context.Context, session *UploadSession) error {
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO upload_sessions
		(id,owner_username,filename,safe_filename,total_size,received_size,sha256,sha1,pre_sha1,target_cid,channel,temp_path,final_path,status,error,upload_task_id,created_at,updated_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		session.ID, session.OwnerUsername, session.Filename, session.SafeFilename, session.TotalSize,
		session.ReceivedSize, session.SHA256, session.SHA1, session.PreSHA1, session.TargetCID,
		session.Channel, session.TempPath, session.FinalPath, session.Status, session.Error,
		session.UploadTaskID, session.CreatedAt, session.UpdatedAt, session.ExpiresAt)
	return err
}

func (s *Store) GetUploadSession(ctx context.Context, id string) (*UploadSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,owner_username,filename,safe_filename,total_size,received_size,sha256,sha1,pre_sha1,target_cid,channel,temp_path,final_path,status,error,upload_task_id,created_at,updated_at,expires_at FROM upload_sessions WHERE id=?`, id)
	return scanUploadSession(row)
}

func (s *Store) UpdateUploadSession(ctx context.Context, session *UploadSession) error {
	session.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE upload_sessions SET received_size=?,sha256=?,sha1=?,pre_sha1=?,target_cid=?,channel=?,temp_path=?,final_path=?,status=?,error=?,upload_task_id=?,updated_at=?,expires_at=? WHERE id=?`,
		session.ReceivedSize, session.SHA256, session.SHA1, session.PreSHA1, session.TargetCID, session.Channel,
		session.TempPath, session.FinalPath, session.Status, Redact(session.Error), session.UploadTaskID,
		session.UpdatedAt, session.ExpiresAt, session.ID)
	return err
}

// AdvanceUploadSession updates the received offset only if the caller used
// the offset currently stored in SQLite. This prevents two PATCH requests from
// claiming the same bytes even if they arrive concurrently.
func (s *Store) AdvanceUploadSession(ctx context.Context, id string, expected, received int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE upload_sessions SET received_size=?,status='receiving',updated_at=? WHERE id=? AND received_size=? AND status IN ('created','receiving')`, received, time.Now().UTC(), id, expected)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) ListUploadSessions(ctx context.Context, owner string, all bool, limit int) ([]UploadSession, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := `SELECT id,owner_username,filename,safe_filename,total_size,received_size,sha256,sha1,pre_sha1,target_cid,channel,temp_path,final_path,status,error,upload_task_id,created_at,updated_at,expires_at FROM upload_sessions`
	args := []any{}
	if !all {
		query += ` WHERE owner_username=?`
		args = append(args, owner)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UploadSession, 0)
	for rows.Next() {
		item, err := scanUploadSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteUploadSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM upload_sessions WHERE id=?`, id)
	return err
}

func (s *Store) ExpiredUploadSessions(ctx context.Context, now time.Time, limit int) ([]UploadSession, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,owner_username,filename,safe_filename,total_size,received_size,sha256,sha1,pre_sha1,target_cid,channel,temp_path,final_path,status,error,upload_task_id,created_at,updated_at,expires_at FROM upload_sessions WHERE expires_at<? AND status IN ('created','receiving','verifying','failed','paused','ready') ORDER BY expires_at LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UploadSession, 0)
	for rows.Next() {
		item, err := scanUploadSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

type uploadSessionScanner interface{ Scan(...any) error }

func scanUploadSession(row uploadSessionScanner) (*UploadSession, error) {
	var item UploadSession
	err := row.Scan(&item.ID, &item.OwnerUsername, &item.Filename, &item.SafeFilename, &item.TotalSize,
		&item.ReceivedSize, &item.SHA256, &item.SHA1, &item.PreSHA1, &item.TargetCID, &item.Channel,
		&item.TempPath, &item.FinalPath, &item.Status, &item.Error, &item.UploadTaskID,
		&item.CreatedAt, &item.UpdatedAt, &item.ExpiresAt)
	return &item, err
}
