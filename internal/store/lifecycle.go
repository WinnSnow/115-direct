package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type Quality struct {
	Resolution int    `json:"resolution"`
	FPS        string `json:"fps,omitempty"`
	HDR        string `json:"hdr,omitempty"`
	Codec      string `json:"codec,omitempty"`
	Audio      string `json:"audio,omitempty"`
}

// Links survive missing sources and explicit deletion. STRM IDs never change on rematch.
type MediaLink struct {
	RemoteID       string    `json:"remote_id"`
	InboxID        string    `json:"inbox_id,omitempty"`
	SourcePath     string    `json:"source_path"`
	OutputPath     string    `json:"output_path"`
	TitlePath      string    `json:"title_path,omitempty"`
	Copies         []string  `json:"copies,omitempty"`
	Mode           string    `json:"mode"`
	Kind           string    `json:"kind"`
	TMDBID         int64     `json:"tmdb_id"`
	Season         int       `json:"season"`
	Episode        int       `json:"episode"`
	EpisodeEnd     int       `json:"episode_end"`
	Part           string    `json:"part,omitempty"`
	VersionGroup   string    `json:"version_group"`
	Quality        Quality   `json:"quality"`
	IngestedAt     time.Time `json:"ingested_at"`
	Manual         bool      `json:"manual"`
	Unavailable    bool      `json:"unavailable"`
	Deleted        string    `json:"deleted,omitempty"`
	Suppressed     string    `json:"suppressed,omitempty"`
	PreviousOutput string    `json:"previous_output,omitempty"`
}

type Execution struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Body      json.RawMessage `json:"body"`
	Error     string          `json:"error,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type DeletionReview struct {
	ID        string    `json:"id"`
	RemoteID  string    `json:"remote_id"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) MediaLink(ctx context.Context, id string) (*MediaLink, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT body FROM media_links WHERE remote_id=?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var link MediaLink
	err := json.Unmarshal(raw, &link)
	return &link, err
}

func (s *Store) PutLink(ctx context.Context, link MediaLink) error {
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO media_links(remote_id,body) VALUES(?,?) ON CONFLICT(remote_id) DO UPDATE SET body=excluded.body`, link.RemoteID, raw)
	return err
}

func (s *Store) ListLinks(ctx context.Context) ([]MediaLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM media_links ORDER BY remote_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []MediaLink{}
	for rows.Next() {
		var raw []byte
		var l MediaLink
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func (s *Store) PutExecution(ctx context.Context, e Execution) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO execution_steps(id,kind,status,body,error,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body,error=excluded.error,updated_at=excluded.updated_at`, e.ID, e.Kind, e.Status, []byte(e.Body), e.Error, time.Now().UTC())
	return err
}

func (s *Store) Execution(ctx context.Context, id string) (*Execution, error) {
	var e Execution
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,status,body,error,updated_at FROM execution_steps WHERE id=?`, id).Scan(&e.ID, &e.Kind, &e.Status, &e.Body, &e.Error, &e.UpdatedAt)
	return &e, err
}

func (s *Store) Executions(ctx context.Context) ([]Execution, error) {
	return s.listExecutions(ctx, `SELECT id,kind,status,body,error,updated_at FROM execution_steps ORDER BY CASE WHEN status='completed' THEN 1 ELSE 0 END, updated_at DESC LIMIT 500`)
}
func (s *Store) RecoverableExecutions(ctx context.Context) ([]Execution, error) {
	return s.listExecutions(ctx, `SELECT id,kind,status,body,error,updated_at FROM execution_steps WHERE status!='completed' AND error='' ORDER BY updated_at`)
}
func (s *Store) listExecutions(ctx context.Context, query string) ([]Execution, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Execution{}
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.Kind, &e.Status, &e.Body, &e.Error, &e.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}

func (s *Store) QueueReview(ctx context.Context, id, remoteID, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO deletion_reviews(id,remote_id,reason,status,updated_at) VALUES(?,?,?,'pending',?) ON CONFLICT(id) DO UPDATE SET reason=excluded.reason,status=CASE WHEN deletion_reviews.status IN ('resolved','restored') THEN 'pending' ELSE deletion_reviews.status END,updated_at=excluded.updated_at`, id, remoteID, reason, time.Now().UTC())
	return err
}

func (s *Store) Reviews(ctx context.Context) ([]DeletionReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,remote_id,reason,status,updated_at FROM deletion_reviews ORDER BY updated_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DeletionReview{}
	for rows.Next() {
		var i DeletionReview
		if err := rows.Scan(&i.ID, &i.RemoteID, &i.Reason, &i.Status, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (s *Store) ResolveReview(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deletion_reviews SET status=?,updated_at=? WHERE id=?`, status, time.Now().UTC(), id)
	return err
}

type Asset struct {
	Path   string   `json:"path"`
	Hash   string   `json:"hash"`
	Owners []string `json:"owners"`
}

func ContentHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s *Store) Asset(ctx context.Context, path string) (*Asset, error) {
	a := &Asset{Path: path}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT hash,owners FROM generated_assets WHERE path=?`, path).Scan(&a.Hash, &raw); err != nil {
		return nil, err
	}
	err := json.Unmarshal(raw, &a.Owners)
	return a, err
}
func (s *Store) PutAsset(ctx context.Context, a Asset) error {
	raw, err := json.Marshal(a.Owners)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO generated_assets(path,hash,owners) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,owners=excluded.owners`, a.Path, a.Hash, raw)
	return err
}
func (s *Store) Assets(ctx context.Context) ([]Asset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,hash,owners FROM generated_assets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Asset{}
	for rows.Next() {
		var a Asset
		var raw []byte
		if err := rows.Scan(&a.Path, &a.Hash, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Owners); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *Store) RemoveAsset(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM generated_assets WHERE path=?`, path)
	return err
}
