package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"github.com/local/115-direct/internal/secure"
)

type Store struct {
	db    *sql.DB
	vault *secure.Vault
}

func IsMissingSetting(err error) bool { return errors.Is(err, sql.ErrNoRows) }

type TransferJob struct {
	ID                string          `json:"id"`
	Source            string          `json:"source"`
	Sender            string          `json:"sender"`
	ShareURL          string          `json:"share_url"`
	ShareCode         string          `json:"share_code"`
	Status            string          `json:"status"`
	StageCID          string          `json:"stage_cid,omitempty"`
	TMDBKind          string          `json:"tmdb_kind,omitempty"`
	TMDBID            int64           `json:"tmdb_id,omitempty"`
	Title             string          `json:"title,omitempty"`
	Expected          json.RawMessage `json:"expected,omitempty"`
	Actual            json.RawMessage `json:"actual,omitempty"`
	Candidates        json.RawMessage `json:"candidates,omitempty"`
	Error             string          `json:"error,omitempty"`
	CleanupAt         *time.Time      `json:"cleanup_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Duplicate         bool            `json:"duplicate,omitempty"`
	SubmissionMessage string          `json:"submission_message,omitempty"`
	ShareUpdate       bool            `json:"share_update,omitempty"`
}

type MediaEntry struct {
	ID         string    `json:"id"`
	RemoteID   string    `json:"remote_id"`
	PickCode   string    `json:"pick_code"`
	SHA1       string    `json:"sha1"`
	Name       string    `json:"name"`
	RemotePath string    `json:"remote_path"`
	STRMPath   string    `json:"strm_path"`
	Missing    int       `json:"missing_count"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Category  string    `json:"category"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func Open(path string, vault *secure.Vault) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		return nil, err
	}
	s := &Store{db: db, vault: vault}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS cms_media (path TEXT PRIMARY KEY, root TEXT NOT NULL, source TEXT NOT NULL, pick_code TEXT NOT NULL, extension TEXT NOT NULL, imported_at DATETIME NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_cms_media_pick ON cms_media(pick_code)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'operator',
			enabled BOOLEAN NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transfer_jobs (
			id TEXT PRIMARY KEY, source TEXT NOT NULL, sender TEXT NOT NULL DEFAULT '', share_url TEXT NOT NULL,
			share_code TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, stage_cid TEXT NOT NULL DEFAULT '',
			tmdb_kind TEXT NOT NULL DEFAULT '', tmdb_id INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '',
			expected BLOB, actual BLOB, candidates BLOB, error TEXT NOT NULL DEFAULT '', cleanup_at DATETIME,
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_transfer_status ON transfer_jobs(status, created_at)`,
		`CREATE TABLE IF NOT EXISTS media_entries (
			id TEXT PRIMARY KEY, remote_id TEXT NOT NULL UNIQUE, pick_code TEXT NOT NULL, sha1 TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL, remote_path TEXT NOT NULL, strm_path TEXT NOT NULL,
			missing_count INTEGER NOT NULL DEFAULT 0, updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_dedup (dedup_key TEXT PRIMARY KEY, created_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS local_organization (remote_id TEXT PRIMARY KEY, path TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS audit_events (id INTEGER PRIMARY KEY AUTOINCREMENT, category TEXT NOT NULL, message TEXT NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS media_links (remote_id TEXT PRIMARY KEY, body BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS execution_steps (id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL, body BLOB NOT NULL, error TEXT NOT NULL DEFAULT '', updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS deletion_reviews (id TEXT PRIMARY KEY, remote_id TEXT NOT NULL, reason TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', updated_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS generated_assets (path TEXT PRIMARY KEY, hash TEXT NOT NULL, owners BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS share_receipts (share_key TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES transfer_jobs(id))`,
		`CREATE TABLE IF NOT EXISTS share_revisions (revision_key TEXT PRIMARY KEY, share_key TEXT NOT NULL, job_id TEXT NOT NULL REFERENCES transfer_jobs(id))`,
		`CREATE TABLE IF NOT EXISTS sync_records (id TEXT PRIMARY KEY, mode TEXT NOT NULL, trigger TEXT NOT NULL, library_cid TEXT NOT NULL, status TEXT NOT NULL, files INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL DEFAULT 0, unavailable INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', started_at DATETIME NOT NULL, finished_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS recognition_cache (key TEXT PRIMARY KEY, kind TEXT NOT NULL, label TEXT NOT NULL, body BLOB NOT NULL, hits INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, last_used_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS upload_tasks (
			id TEXT PRIMARY KEY, local_path TEXT NOT NULL, target_cid TEXT NOT NULL, filename TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0, sha1 TEXT NOT NULL, pre_sha1 TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, remote_id TEXT NOT NULL DEFAULT '',
			pick_code TEXT NOT NULL DEFAULT '', rapid BOOLEAN NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, finished_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_upload_tasks_status ON upload_tasks(status, created_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_upload_tasks_dedupe ON upload_tasks(local_path,target_cid,sha1,size)`,
		`CREATE TABLE IF NOT EXISTS upload_sessions (
			id TEXT PRIMARY KEY, owner_username TEXT NOT NULL DEFAULT '', filename TEXT NOT NULL,
			safe_filename TEXT NOT NULL, total_size INTEGER NOT NULL, received_size INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT NOT NULL DEFAULT '', sha1 TEXT NOT NULL DEFAULT '', pre_sha1 TEXT NOT NULL DEFAULT '',
			target_cid TEXT NOT NULL DEFAULT '', channel TEXT NOT NULL DEFAULT 'auto',
			temp_path TEXT NOT NULL, final_path TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '', upload_task_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, expires_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_upload_sessions_owner_status ON upload_sessions(owner_username,status,updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_upload_sessions_expiry ON upload_sessions(expires_at,status)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := s.migrateShareReceipts(); err != nil {
		return err
	}
	return s.migrateLogs()
}

func (s *Store) PutSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sealed, err := s.vault.Seal(string(raw))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, sealed, time.Now().UTC())
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string, out any) error {
	var sealed string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&sealed); err != nil {
		return err
	}
	plain, err := s.vault.Open(sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(plain), out)
}

func (s *Store) CreateJob(ctx context.Context, job *TransferJob) error {
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO transfer_jobs
		(id,source,sender,share_url,share_code,status,tmdb_kind,tmdb_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Source, job.Sender, job.ShareURL, job.ShareCode,
		job.Status, job.TMDBKind, job.TMDBID, now, now)
	return err
}

func (s *Store) UpdateJob(ctx context.Context, job *TransferJob) error {
	job.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE transfer_jobs SET status=?,stage_cid=?,tmdb_kind=?,tmdb_id=?,title=?,
		expected=?,actual=?,candidates=?,error=?,cleanup_at=?,updated_at=? WHERE id=?`, job.Status, job.StageCID,
		job.TMDBKind, job.TMDBID, job.Title, []byte(job.Expected), []byte(job.Actual), []byte(job.Candidates),
		job.Error, job.CleanupAt, job.UpdatedAt, job.ID)
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (*TransferJob, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,source,sender,share_url,share_code,status,stage_cid,tmdb_kind,tmdb_id,title,
		expected,actual,candidates,error,cleanup_at,created_at,updated_at FROM transfer_jobs WHERE id=?`, id)
	return scanJob(row)
}

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (*TransferJob, error) {
	var j TransferJob
	var expected, actual, candidates []byte
	var cleanup sql.NullTime
	err := row.Scan(&j.ID, &j.Source, &j.Sender, &j.ShareURL, &j.ShareCode, &j.Status, &j.StageCID,
		&j.TMDBKind, &j.TMDBID, &j.Title, &expected, &actual, &candidates, &j.Error, &cleanup, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	j.Expected, j.Actual, j.Candidates = expected, actual, candidates
	if cleanup.Valid {
		j.CleanupAt = &cleanup.Time
	}
	return &j, nil
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]TransferJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,source,sender,share_url,share_code,status,stage_cid,tmdb_kind,tmdb_id,title,
		expected,actual,candidates,error,cleanup_at,created_at,updated_at FROM transfer_jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]TransferJob, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func (s *Store) QueuedJobs(ctx context.Context) ([]TransferJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source,sender,share_url,share_code,status,stage_cid,tmdb_kind,tmdb_id,title,
		expected,actual,candidates,error,cleanup_at,created_at,updated_at FROM transfer_jobs
		WHERE status IN ('queued','retry','received','transferring','matching','organizing','verifying') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []TransferJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

func (s *Store) PutMedia(ctx context.Context, entry MediaEntry) error {
	entry.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO media_entries(id,remote_id,pick_code,sha1,name,remote_path,strm_path,missing_count,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(remote_id) DO UPDATE SET pick_code=excluded.pick_code,sha1=excluded.sha1,
		name=excluded.name,remote_path=excluded.remote_path,strm_path=excluded.strm_path,missing_count=0,updated_at=excluded.updated_at`,
		entry.ID, entry.RemoteID, entry.PickCode, entry.SHA1, entry.Name, entry.RemotePath, entry.STRMPath, entry.Missing, entry.UpdatedAt)
	return err
}

func (s *Store) GetMedia(ctx context.Context, id string) (*MediaEntry, error) {
	var m MediaEntry
	err := s.db.QueryRowContext(ctx, `SELECT id,remote_id,pick_code,sha1,name,remote_path,strm_path,missing_count,updated_at
		FROM media_entries WHERE id=?`, id).Scan(&m.ID, &m.RemoteID, &m.PickCode, &m.SHA1, &m.Name, &m.RemotePath, &m.STRMPath, &m.Missing, &m.UpdatedAt)
	return &m, err
}

func (s *Store) GetMediaByRemoteID(ctx context.Context, remoteID string) (*MediaEntry, error) {
	var m MediaEntry
	err := s.db.QueryRowContext(ctx, `SELECT id,remote_id,pick_code,sha1,name,remote_path,strm_path,missing_count,updated_at
		FROM media_entries WHERE remote_id=?`, remoteID).Scan(&m.ID, &m.RemoteID, &m.PickCode, &m.SHA1, &m.Name,
		&m.RemotePath, &m.STRMPath, &m.Missing, &m.UpdatedAt)
	return &m, err
}

func (s *Store) ListMedia(ctx context.Context) ([]MediaEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,remote_id,pick_code,sha1,name,remote_path,strm_path,missing_count,updated_at FROM media_entries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []MediaEntry
	for rows.Next() {
		var m MediaEntry
		if err := rows.Scan(&m.ID, &m.RemoteID, &m.PickCode, &m.SHA1, &m.Name, &m.RemotePath, &m.STRMPath, &m.Missing, &m.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, m)
	}
	return entries, rows.Err()
}

func (s *Store) IncrementMediaMissing(ctx context.Context, remoteID string) (int, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE media_entries SET missing_count=missing_count+1,updated_at=? WHERE remote_id=?`, time.Now().UTC(), remoteID); err != nil {
		return 0, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT missing_count FROM media_entries WHERE remote_id=?`, remoteID).Scan(&count)
	return count, err
}

func (s *Store) DeleteMedia(ctx context.Context, remoteID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM media_entries WHERE remote_id=?`, remoteID)
	return err
}

func (s *Store) LocalOrganization(ctx context.Context, remoteID string) (string, error) {
	var path string
	err := s.db.QueryRowContext(ctx, `SELECT path FROM local_organization WHERE remote_id=?`, remoteID).Scan(&path)
	return path, err
}

func (s *Store) SetLocalOrganization(ctx context.Context, remoteID, path string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO local_organization(remote_id,path) VALUES(?,?) ON CONFLICT(remote_id) DO UPDATE SET path=excluded.path`, remoteID, path)
	return err
}

func (s *Store) DeduplicateMessage(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, errors.New("empty dedup key")
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO message_dedup(dedup_key,created_at) VALUES(?,?)`, key, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (s *Store) Audit(ctx context.Context, category, message string) {
	s.Log(ctx, category, "info", message)
}

func (s *Store) ListAudit(ctx context.Context, category string, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `SELECT id,category,message,created_at FROM audit_events`
	args := []any{}
	if category != "" {
		query += ` WHERE category=?`
		args = append(args, category)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.Category, &event.Message, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for key, query := range map[string]string{
		"jobs":    `SELECT COUNT(*) FROM transfer_jobs`,
		"pending": `SELECT COUNT(*) FROM transfer_jobs WHERE status IN ('queued','transferring','received','matching','waiting_match','organizing','verifying','retry')`,
		"failed":  `SELECT COUNT(*) FROM transfer_jobs WHERE status='failed'`,
		"media":   `SELECT COUNT(*) FROM media_entries`,
	} {
		var count int64
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, err
		}
		out[key] = count
	}
	return out, nil
}
