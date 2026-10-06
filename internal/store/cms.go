package store

import (
	"context"
	"time"
)

type CMSMedia struct {
	Path       string    `json:"path"`
	Root       string    `json:"root"`
	Source     string    `json:"source"`
	PickCode   string    `json:"pick_code"`
	Extension  string    `json:"extension"`
	ImportedAt time.Time `json:"imported_at"`
}

func (s *Store) ImportCMSMedia(ctx context.Context, items []CMSMedia) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		_, err = tx.ExecContext(ctx, `INSERT INTO cms_media(path,root,source,pick_code,extension,imported_at) VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET root=excluded.root,source=excluded.source,pick_code=excluded.pick_code,extension=excluded.extension`, item.Path, item.Root, item.Source, item.PickCode, item.Extension, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) HasCMSPick(ctx context.Context, pick string) (bool, error) {
	var yes bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cms_media WHERE pick_code=?)`, pick).Scan(&yes)
	return yes, err
}
func (s *Store) ListCMSMedia(ctx context.Context, limit, offset int) ([]CMSMedia, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cms_media`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path,root,source,pick_code,extension,imported_at FROM cms_media ORDER BY path LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []CMSMedia{}
	for rows.Next() {
		var m CMSMedia
		if err = rows.Scan(&m.Path, &m.Root, &m.Source, &m.PickCode, &m.Extension, &m.ImportedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, m)
	}
	return items, total, rows.Err()
}
