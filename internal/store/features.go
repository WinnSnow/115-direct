package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var shareLinkRE = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:115\.com|115cdn\.com|anxia\.com)/s/([A-Za-z0-9_-]+)`)

const jobColumns = `id,source,sender,share_url,share_code,status,stage_cid,tmdb_kind,tmdb_id,title,expected,actual,candidates,error,cleanup_at,created_at,updated_at`

func ShareKey(link string) string {
	m := shareLinkRE.FindStringSubmatch(strings.TrimSpace(link))
	if len(m) != 2 {
		return ""
	}
	return ContentHash([]byte("115-share:" + m[1]))
}

func (s *Store) migrateShareReceipts() error {
	rows, err := s.db.Query(`SELECT id,share_url FROM transfer_jobs WHERE source IN ('web','wecom') ORDER BY CASE WHEN status='completed' THEN 0 ELSE 1 END,created_at`)
	if err != nil {
		return err
	}
	type receipt struct{ id, key string }
	var items []receipt
	for rows.Next() {
		var id, link string
		if err := rows.Scan(&id, &link); err != nil {
			rows.Close()
			return err
		}
		if key := ShareKey(link); key != "" {
			items = append(items, receipt{id, key})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range items {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO share_receipts(share_key,job_id) VALUES(?,?)`, r.key, r.id); err != nil {
			return err
		}
	}
	return nil
}

// The receipt and job are reserved together, including concurrent submissions.
func (s *Store) CreateShareJob(ctx context.Context, job *TransferJob) (*TransferJob, error) {
	key := ShareKey(job.ShareURL)
	if key == "" {
		return nil, fmt.Errorf("请输入有效的115分享链接")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM share_receipts WHERE share_key=?`, key).Scan(&existing)
	if err == nil {
		tx.Rollback()
		old, err := s.GetJob(ctx, existing)
		if err == nil {
			old.Duplicate = true
		}
		return old, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	_, err = tx.ExecContext(ctx, `INSERT INTO transfer_jobs(id,source,sender,share_url,share_code,status,tmdb_kind,tmdb_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Source, job.Sender, job.ShareURL, job.ShareCode, job.Status, job.TMDBKind, job.TMDBID, now, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO share_receipts(share_key,job_id) VALUES(?,?)`, key, job.ID); err != nil {
		return nil, err
	}
	return job, tx.Commit()
}

type RecordFilter struct {
	Status, Search string
	Limit, Offset  int
}

func recordPage(f RecordFilter) RecordFilter {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}
func (s *Store) TransferRecords(ctx context.Context, filter RecordFilter) ([]TransferJob, int, error) {
	f := recordPage(filter)
	where := ` WHERE source IN ('web','wecom')`
	args := []any{}
	if f.Status != "" {
		where += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.Search != "" {
		where += ` AND (title LIKE ? OR id LIKE ? OR share_url LIKE ?)`
		for i := 0; i < 3; i++ {
			args = append(args, "%"+f.Search+"%")
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transfer_jobs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM transfer_jobs`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []TransferJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *j)
	}
	return items, total, rows.Err()
}

type SyncRecord struct {
	ID          string     `json:"id"`
	Mode        string     `json:"mode"`
	Trigger     string     `json:"trigger"`
	LibraryCID  string     `json:"library_cid"`
	Status      string     `json:"status"`
	Files       int        `json:"files"`
	Created     int        `json:"created"`
	Unavailable int        `json:"unavailable"`
	Error       string     `json:"error,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

func (s *Store) PutSyncRecord(ctx context.Context, r SyncRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_records(id,mode,trigger,library_cid,status,files,created,unavailable,error,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,files=excluded.files,created=excluded.created,unavailable=excluded.unavailable,error=excluded.error,finished_at=excluded.finished_at`, r.ID, r.Mode, r.Trigger, r.LibraryCID, r.Status, r.Files, r.Created, r.Unavailable, Redact(r.Error), r.StartedAt, r.FinishedAt)
	return err
}
func (s *Store) RecoverSyncRecords(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_records SET status='interrupted',error='服务重启，扫描已中断；可重新同步',finished_at=? WHERE status='running'`, time.Now().UTC())
	return err
}
func (s *Store) SyncRecords(ctx context.Context, filter RecordFilter) ([]SyncRecord, int, error) {
	f := recordPage(filter)
	where := ` WHERE 1=1`
	args := []any{}
	if f.Status != "" {
		where += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.Search != "" {
		where += ` AND (id LIKE ? OR error LIKE ? OR library_cid LIKE ?)`
		for i := 0; i < 3; i++ {
			args = append(args, "%"+f.Search+"%")
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_records`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,mode,trigger,library_cid,status,files,created,unavailable,error,started_at,finished_at FROM sync_records`+where+` ORDER BY started_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []SyncRecord{}
	for rows.Next() {
		var r SyncRecord
		if err := rows.Scan(&r.ID, &r.Mode, &r.Trigger, &r.LibraryCID, &r.Status, &r.Files, &r.Created, &r.Unavailable, &r.Error, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, r)
	}
	return items, total, rows.Err()
}

type CacheConfig struct {
	Enabled    bool `json:"enabled"`
	TTLDays    int  `json:"ttl_days"`
	MaxEntries int  `json:"max_entries"`
	MaxBytes   int  `json:"max_bytes"`
}

func DefaultCacheConfig() CacheConfig { return CacheConfig{true, 30, 5000, 64 << 20} }
func (c CacheConfig) Validate() error {
	if c.TTLDays < 1 || c.TTLDays > 365 || c.MaxEntries < 10 || c.MaxEntries > 10000 || c.MaxBytes < 1<<20 || c.MaxBytes > 256<<20 {
		return fmt.Errorf("缓存有效期1至365天，条数10至10000，容量1至256MB")
	}
	return nil
}
func (s *Store) CacheOptions(ctx context.Context) CacheConfig {
	c := DefaultCacheConfig()
	_ = s.GetSetting(ctx, "recognition", &c)
	return c
}

type CacheEntry struct {
	Key        string    `json:"key"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label"`
	Hits       int       `json:"hits"`
	Bytes      int       `json:"bytes"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

func (s *Store) Cached(ctx context.Context, key string) ([]byte, bool, error) {
	if !s.CacheOptions(ctx).Enabled {
		return nil, false, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM recognition_cache WHERE key=? AND expires_at>?`, key, time.Now().UTC()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE recognition_cache SET hits=hits+1,last_used_at=? WHERE key=?`, time.Now().UTC(), key)
	return raw, true, err
}
func (s *Store) PutCached(ctx context.Context, key, kind, label string, raw []byte) error {
	c := s.CacheOptions(ctx)
	if !c.Enabled {
		return nil
	}
	if !json.Valid(raw) || len(raw) > 4<<20 {
		return fmt.Errorf("缓存内容无效或过大")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO recognition_cache(key,kind,label,body,created_at,expires_at,last_used_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET kind=excluded.kind,label=excluded.label,body=excluded.body,created_at=excluded.created_at,expires_at=excluded.expires_at,last_used_at=excluded.last_used_at`, key, kind, label, raw, now, now.Add(time.Duration(c.TTLDays)*24*time.Hour), now)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM recognition_cache WHERE expires_at<=? OR key IN (SELECT key FROM recognition_cache ORDER BY last_used_at DESC LIMIT -1 OFFSET ?)`, now, c.MaxEntries)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM recognition_cache WHERE key IN (SELECT key FROM (SELECT key,SUM(length(body)) OVER (ORDER BY last_used_at DESC,key) AS bytes FROM recognition_cache) WHERE bytes>?)`, c.MaxBytes)
	return err
}
func (s *Store) CacheEntries(ctx context.Context, filter RecordFilter) ([]CacheEntry, int, error) {
	f := recordPage(filter)
	where := ` WHERE 1=1`
	args := []any{}
	if f.Search != "" {
		where += ` AND label LIKE ?`
		args = append(args, "%"+f.Search+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recognition_cache`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT key,kind,label,hits,length(body),created_at,expires_at,last_used_at FROM recognition_cache`+where+` ORDER BY last_used_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []CacheEntry{}
	for rows.Next() {
		var e CacheEntry
		if err := rows.Scan(&e.Key, &e.Kind, &e.Label, &e.Hits, &e.Bytes, &e.CreatedAt, &e.ExpiresAt, &e.LastUsedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, e)
	}
	return items, total, rows.Err()
}
func (s *Store) DeleteCached(ctx context.Context, key string) error {
	if key == "all" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM recognition_cache`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM recognition_cache WHERE key=?`, key)
	return err
}
