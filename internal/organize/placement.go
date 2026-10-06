package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type placement struct {
	CID       string          `json:"cid"`
	Path      string          `json:"path"`
	TargetCID string          `json:"target_cid,omitempty"`
	Name      string          `json:"name,omitempty"`
	Before    []string        `json:"before,omitempty"`
	Pending   bool            `json:"pending,omitempty"`
	Inventory []placementFile `json:"inventory,omitempty"`
}

type placementFile struct {
	Relative string `json:"relative"`
	Size     int64  `json:"size"`
	SHA1     string `json:"sha1"`
}

func placementInventory(files []sourceFile) []placementFile {
	result := []placementFile{}
	for _, f := range files {
		if classify(f.Entry.Name) == Video {
			result = append(result, placementFile{Relative: f.Relative, Size: f.Entry.Size, SHA1: f.Entry.SHA1})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Relative < result[j].Relative })
	return result
}

func (s *Service) waitPlacedFiles(ctx context.Context, saved placement) ([]sourceFile, error) {
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		files, err := s.walk(ctx, saved.CID, "")
		if err == nil {
			got := placementInventory(files)
			matches := len(got) == len(saved.Inventory)
			if matches {
				for i := range got {
					if got[i] != saved.Inventory[i] {
						matches = false
						break
					}
				}
			}
			if matches {
				for i := range files {
					files[i].Relative = filepath.Join(saved.Path, files[i].Relative)
				}
				return files, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("115 分类副本内容尚未完整，请核对后续跑；不会重复复制")
		case <-time.After(time.Second):
		}
	}
}

// Only one directory copy is needed; source names and media bytes remain unchanged.
func (s *Service) placeReceived(ctx context.Context, cfg DirectoryConfig, job *store.TransferJob, details *tmdb.Details) ([]sourceFile, error) {
	key := "placement:" + job.ID
	var saved placement
	err := s.Store.GetSetting(ctx, key, &saved)
	if err == nil && saved.CID != "" && len(saved.Inventory) > 0 {
		return s.waitPlacedFiles(ctx, saved)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	category, subcategory := s.mediaDirectories(ctx, details)
	if subcategory == "" {
		return nil, fmt.Errorf("分类不明确，需要人工指定")
	}
	catCID, err := s.ensureDir(ctx, cfg.LibraryCID, category)
	if err != nil {
		return nil, err
	}
	targetCID, err := s.ensureDir(ctx, catCID, subcategory)
	if err != nil {
		return nil, err
	}
	children, err := s.Pan.List(ctx, job.StageCID)
	if err != nil {
		return nil, err
	}
	parents, err := s.Pan.List(ctx, cfg.InboxCID)
	if err != nil {
		return nil, err
	}
	var source pan115.Entry
	for _, entry := range parents {
		if entry.ID == job.StageCID && entry.Directory {
			source = entry
			break
		}
	}
	if source.ID == "" {
		return nil, fmt.Errorf("接收目录不在配置的接收位置")
	}
	// Unwrap only our receive staging folder, never a work's sole season folder.
	if isTemporaryStageName(source.Name, job.ID) && len(children) == 1 && children[0].Directory {
		source = children[0]
	}
	sourceFiles, err := s.walk(ctx, source.ID, "")
	if err != nil {
		return nil, err
	}
	inventory := placementInventory(sourceFiles)
	if len(inventory) == 0 {
		return nil, fmt.Errorf("接收目录没有可归档的视频")
	}
	if saved.CID != "" {
		saved.Inventory = inventory
		if err := s.Store.PutSetting(ctx, key, saved); err != nil {
			return nil, err
		}
		return s.waitPlacedFiles(ctx, saved)
	}
	seen := map[string]bool{}
	placementPath := filepath.Join(category, subcategory, source.Name)
	if saved.Pending {
		targetCID, source.Name = saved.TargetCID, saved.Name
		if saved.Path != "" {
			placementPath = saved.Path
		}
		for _, id := range saved.Before {
			seen[id] = true
		}
	} else {
		before, err := s.Pan.List(ctx, targetCID)
		if err != nil {
			return nil, err
		}
		for _, entry := range before {
			if entry.Name == source.Name {
				wrapper := source.Name + " [v-" + stableSuffix(job.ID) + "]"
				targetCID, err = s.ensureDir(ctx, targetCID, wrapper)
				if err != nil {
					return nil, err
				}
				placementPath = filepath.Join(category, subcategory, wrapper, source.Name)
				before, err = s.Pan.List(ctx, targetCID)
				if err != nil {
					return nil, err
				}
				break
			}
		}
		saved = placement{TargetCID: targetCID, Name: source.Name, Path: placementPath, Pending: true, Inventory: inventory}
		for _, entry := range before {
			seen[entry.ID] = true
			saved.Before = append(saved.Before, entry.ID)
			if entry.Directory && entry.Name == source.Name {
				return nil, fmt.Errorf("115 本次版本目录已存在副本 %s，请核对后重试", source.Name)
			}
		}
		// Persist the attempt before calling 115 so retries never repeat an uncertain copy.
		if err := s.Store.PutSetting(ctx, key, saved); err != nil {
			return nil, err
		}
		if err := s.Pan.Copy(ctx, targetCID, source.ID); err != nil {
			return nil, err
		}
	}
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		entries, err := s.Pan.List(ctx, targetCID)
		if err == nil {
			for _, entry := range entries {
				if seen[entry.ID] || !entry.Directory || entry.Name != source.Name {
					continue
				}
				saved = placement{CID: entry.ID, Path: placementPath, Inventory: inventory}
				if err := s.Store.PutSetting(ctx, key, saved); err != nil {
					return nil, err
				}
				s.Store.Audit(ctx, "library", fmt.Sprintf("115 分类归档 %s/%s/%s；接收原件保留，未上传刮削信息", category, subcategory, entry.Name))
				return s.waitPlacedFiles(ctx, saved)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("115 分类副本尚未出现，请先检查目标目录再重试")
		case <-time.After(3 * time.Second):
		}
	}
}
