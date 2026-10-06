package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/local/115-direct/internal/store"
)

type assetStep struct {
	Path    string   `json:"path"`
	Data    []byte   `json:"data"`
	OldHash string   `json:"old_hash"`
	Owners  []string `json:"owners"`
	Root    string   `json:"root"`
}

type imageStep struct {
	Root   string   `json:"root"`
	Dir    string   `json:"dir"`
	Name   string   `json:"name"`
	Remote string   `json:"remote"`
	Owners []string `json:"owners"`
}

func (s *Service) resumeImage(ctx context.Context, e *store.Execution) (result error) {
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var a imageStep
	if err := json.Unmarshal(e.Body, &a); err != nil {
		return err
	}
	if !within(a.Root, filepath.Join(a.Dir, a.Name)) {
		return fmt.Errorf("图片路径越界")
	}
	if err := s.checkAssetOwners(ctx, filepath.Join(a.Dir, a.Name), a.Owners); err != nil {
		return err
	}
	if e.Status == "completed" {
		if _, err := os.Stat(filepath.Join(a.Dir, a.Name)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		e.Status = "planned"
		if err := s.Store.PutExecution(ctx, *e); err != nil {
			return err
		}
	}
	if err := validateMetadataDir(a.Root, a.Dir); err != nil {
		return err
	}
	data, err := s.TMDB.Image(ctx, a.Remote)
	if err != nil {
		return err
	}
	if err := s.writeAsset(ctx, a.Root, filepath.Join(a.Dir, a.Name), data, a.Owners); err != nil {
		return err
	}
	e.Status = "completed"
	e.Error = ""
	return s.Store.PutExecution(ctx, *e)
}

func (s *Service) writeOwnedNFO(ctx context.Context, root, path string, nfo metadataNFO, owners []string) error {
	nfo.Generator = "115 Direct"
	raw, err := xml.MarshalIndent(nfo, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAsset(ctx, root, path, append([]byte(xml.Header), append(raw, '\n')...), owners)
}
func (s *Service) writeAsset(ctx context.Context, root, path string, data []byte, owners []string) error {
	a, err := s.Store.Asset(ctx, path)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	oldHash := ""
	if a != nil {
		oldHash = a.Hash
		for _, owner := range a.Owners {
			found := false
			for _, v := range owners {
				if v == owner {
					found = true
				}
			}
			if !found {
				owners = append(owners, owner)
			}
		}
	}
	existing, err := os.ReadFile(path)
	if err == nil {
		if a == nil || store.ContentHash(existing) != oldHash {
			s.Store.Audit(ctx, "scrape", "保留用户元数据: "+path)
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	body, _ := json.Marshal(assetStep{path, data, oldHash, owners, root})
	id := "asset:" + store.ContentHash(body)
	e, err := s.Store.Execution(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		e = &store.Execution{ID: id, Kind: "asset", Status: "planned", Body: body}
		if err := s.Store.PutExecution(ctx, *e); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if e.Status == "completed" {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			e.Status = "planned"
			if err := s.Store.PutExecution(ctx, *e); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			return s.Store.PutAsset(ctx, store.Asset{Path: path, Hash: store.ContentHash(data), Owners: owners})
		}
	}
	return s.resumeAsset(ctx, e)
}
func (s *Service) resumeAsset(ctx context.Context, e *store.Execution) (result error) {
	if e.Status == "completed" {
		return nil
	}
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var a assetStep
	if err := json.Unmarshal(e.Body, &a); err != nil {
		return err
	}
	if !within(a.Root, a.Path) {
		return fmt.Errorf("元数据路径越界")
	}
	if err := s.checkAssetOwners(ctx, a.Path, a.Owners); err != nil {
		return err
	}
	if err := validateMetadataDir(a.Root, filepath.Dir(a.Path)); err != nil {
		return err
	}
	data, err := os.ReadFile(a.Path)
	if err == nil && store.ContentHash(data) != a.OldHash && store.ContentHash(data) != store.ContentHash(a.Data) {
		return fmt.Errorf("元数据已被修改，保留用户文件")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := writeAtomic(a.Path, a.Data); err != nil {
		return err
	}
	if err := s.Store.PutAsset(ctx, store.Asset{Path: a.Path, Hash: store.ContentHash(a.Data), Owners: a.Owners}); err != nil {
		return err
	}
	e.Status = "completed"
	e.Error = ""
	return s.Store.PutExecution(ctx, *e)
}

func (s *Service) checkAssetOwners(ctx context.Context, path string, owners []string) error {
	if len(owners) == 0 {
		return nil
	}
	for _, owner := range owners {
		l, err := s.Store.MediaLink(ctx, owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if l.Deleted != "" || l.OutputPath == "" {
			continue
		}
		if l.TitlePath != "" && within(l.TitlePath, path) {
			return nil
		}
		if filepath.Dir(l.OutputPath) == filepath.Dir(path) || filepath.Dir(filepath.Dir(l.OutputPath)) == filepath.Dir(path) {
			return nil
		}
	}
	return fmt.Errorf("元数据关联已删除或移出，暂停重建")
}
