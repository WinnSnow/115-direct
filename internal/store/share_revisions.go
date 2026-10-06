package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Store) ShareJob(ctx context.Context, link string) (*TransferJob, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT job_id FROM share_receipts WHERE share_key=?`, ShareKey(link)).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetJob(ctx, id)
}

// CompletedShareJobs returns completed share jobs that can provide a durable
// file-list baseline when a provider regenerates a share URL/code.
func (s *Store) CompletedShareJobs(ctx context.Context, kind string, tmdbID int64, limit int) ([]TransferJob, error) {
	if kind == "" || tmdbID <= 0 {
		return nil, nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM transfer_jobs
		WHERE source IN ('web','wecom') AND status IN ('completed','cleaned')
		AND tmdb_kind=? AND tmdb_id=? ORDER BY updated_at DESC LIMIT ?`, kind, tmdbID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []TransferJob{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

// Reserve a content revision and its encrypted, frozen selection atomically.
// A newer or unfinished receipt prevents a competing submission from receiving again.
func (s *Store) CreateShareRevision(ctx context.Context, job *TransferJob, fingerprint, previous string, selection any) (*TransferJob, error) {
	key := ShareKey(job.ShareURL)
	if key == "" || fingerprint == "" {
		return nil, fmt.Errorf("分享内容指纹无效")
	}
	raw, err := json.Marshal(selection)
	if err != nil {
		return nil, err
	}
	sealed, err := s.vault.Seal(string(raw))
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	revision := ContentHash([]byte(key + ":" + fingerprint))
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM share_revisions WHERE revision_key=?`, revision).Scan(&existing)
	duplicate := func(id string) (*TransferJob, error) {
		_ = tx.Rollback()
		old, err := s.GetJob(ctx, id)
		if old != nil {
			old.Duplicate = true
		}
		return old, err
	}
	if err == nil {
		return duplicate(existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM share_receipts WHERE share_key=?`, key).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// A new share code may be linked to a completed previous job. In that case
	// there is no receipt for the new key yet, so previous is allowed to be the
	// baseline instead of being treated as a duplicate.
	if existing != "" && existing != previous {
		return duplicate(existing)
	}
	if existing != "" {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM transfer_jobs WHERE id=?`, existing).Scan(&status); err != nil {
			return nil, err
		}
		if status != "completed" && status != "cleaned" {
			return duplicate(existing)
		}
	}
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	_, err = tx.ExecContext(ctx, `INSERT INTO transfer_jobs(id,source,sender,share_url,share_code,status,tmdb_kind,tmdb_id,title,expected,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Source, job.Sender, job.ShareURL, job.ShareCode, job.Status, job.TMDBKind, job.TMDBID, job.Title, []byte(job.Expected), now, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)`, "share_selection:"+job.ID, sealed, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO share_revisions(revision_key,share_key,job_id) VALUES(?,?,?)`, revision, key, job.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO share_receipts(share_key,job_id) VALUES(?,?) ON CONFLICT(share_key) DO UPDATE SET job_id=excluded.job_id`, key, job.ID); err != nil {
		return nil, err
	}
	return job, tx.Commit()
}
