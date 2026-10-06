package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type uploadArchivePlan struct {
	OriginalID  string `json:"original_id"`
	ParentID    string `json:"parent_id"`
	Category    string `json:"category"`
	Subcategory string `json:"subcategory"`
	Relative    string `json:"relative"`
	Size        int64  `json:"size"`
}
type uploadArchiveStep struct {
	Config    DirectoryConfig `json:"config"`
	Item      PlanItem        `json:"item"`
	TargetCID string          `json:"target_cid"`
	Copy      pan115.Entry    `json:"copy"`
	OldPath   string          `json:"old_path"`
}

func (s *Service) previewUploadArchive(ctx context.Context, c DirectoryConfig, m store.MediaEntry, l store.MediaLink, category, sub string) (*uploadArchivePlan, error) {
	if c.LibraryCID == "" || c.LibraryCID == c.InboxCID || sub == "" {
		return nil, fmt.Errorf("上传分类需要有效的115整理目录及二级分类")
	}
	task, err := s.Store.UploadedSource(ctx, m.RemoteID)
	if err != nil {
		return nil, fmt.Errorf("上传来源记录缺失: %w", err)
	}
	entries, err := s.Pan.List(ctx, task.TargetCID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, e := range entries {
		if e.ID == m.RemoteID && !e.Directory && e.Size == task.Size && strings.EqualFold(e.SHA1, m.SHA1) && e.Name == m.Name {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("115上传原件已变化，请核对来源")
	}
	if !validCloudName(m.Name) {
		return nil, fmt.Errorf("上传文件名称无效")
	}
	rel := filepath.Join(category, sub, m.Name)
	path := filepath.Join(c.PendingPath, strings.TrimSuffix(rel, filepath.Ext(rel))+".strm")
	if err := checkDestination(c.PendingPath, path); err != nil {
		return nil, err
	}
	if path != l.SourcePath {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return nil, fmt.Errorf("分类待整理目标已存在或不可访问")
		}
	}
	return &uploadArchivePlan{OriginalID: m.RemoteID, ParentID: task.TargetCID, Category: category, Subcategory: sub, Relative: rel, Size: task.Size}, nil
}

func (s *Service) archiveUploadItem(ctx context.Context, c DirectoryConfig, jobID string, item *PlanItem) error {
	id := "upload_archive:" + jobID + ":" + item.Upload.OriginalID
	e, err := s.Store.Execution(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		raw, _ := json.Marshal(uploadArchiveStep{Config: c, Item: *item, OldPath: item.Previous.SourcePath})
		e = &store.Execution{ID: id, Kind: "upload_archive", Status: "planned", Body: raw}
		if err := s.Store.PutExecution(ctx, *e); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := s.resumeUploadArchive(ctx, e); err != nil {
		return err
	}
	var step uploadArchiveStep
	if err := json.Unmarshal(e.Body, &step); err != nil {
		return err
	}
	*item = step.Item
	return nil
}

func (s *Service) persistArchivedPlan(ctx context.Context, jobID string, plan *OrganizePlan) error {
	j, err := s.Store.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if j.Source == "manual" {
		j.Actual, _ = json.Marshal(plan)
		return s.Store.UpdateJob(ctx, j)
	}
	j.Expected, _ = json.Marshal(plan.Request.IDs)
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return err
	}
	return s.Store.PutSetting(ctx, "organize_plan:"+jobID, plan)
}

func (s *Service) resumeUploadArchive(ctx context.Context, e *store.Execution) (result error) {
	if e.Status == "completed" {
		return nil
	}
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var step uploadArchiveStep
	if err := json.Unmarshal(e.Body, &step); err != nil {
		return err
	}
	a := step.Item.Upload
	if a == nil || step.Item.Previous == nil {
		return fmt.Errorf("上传分类步骤无效")
	}
	c := step.Config
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return err
	}
	set := func(status string) error {
		e.Status = status
		e.Error = ""
		e.Body, _ = json.Marshal(step)
		return s.Store.PutExecution(ctx, *e)
	}
	if e.Status == "planned" {
		cat, err := s.ensureDir(ctx, c.LibraryCID, a.Category)
		if err != nil {
			return err
		}
		target, err := s.ensureDir(ctx, cat, a.Subcategory)
		if err != nil {
			return err
		}
		step.TargetCID = target
		children, err := s.Pan.List(ctx, target)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.Name == step.Item.Media.Name {
				return fmt.Errorf("115分类目标同名项已存在，请核对关联")
			}
		}
		if err := set("copy_pending"); err != nil {
			return err
		}
		if err := s.Pan.Copy(ctx, target, a.OriginalID); err != nil {
			return err
		}
	}
	if e.Status == "copy_pending" {
		children, err := s.Pan.List(ctx, step.TargetCID)
		if err != nil {
			return err
		}
		matches := []pan115.Entry{}
		for _, child := range children {
			if !child.Directory && child.Name == step.Item.Media.Name && child.Size == a.Size && strings.EqualFold(child.SHA1, step.Item.Media.SHA1) {
				matches = append(matches, child)
			}
		}
		if len(matches) != 1 {
			return fmt.Errorf("115分类复制结果尚未确认，重试只核对，不重复复制")
		}
		step.Copy = matches[0]
		if err := set("copied"); err != nil {
			return err
		}
	}
	if e.Status == "copied" {
		m := step.Item.Media
		m.RemoteID = step.Copy.ID
		m.PickCode = step.Copy.PickCode
		m.RemotePath = a.Relative
		l := *step.Item.Previous
		l.RemoteID = m.RemoteID
		l.InboxID = a.OriginalID
		l.SourcePath = step.Item.Link.SourcePath
		l.Kind = step.Item.Link.Kind
		l.TMDBID = step.Item.Link.TMDBID
		l.Manual = step.Item.Link.Manual
		l.Mode = "copy"
		content := s.mediaContent(c, m)
		if err := EnsureLocalDirectory(c.PendingPath, filepath.Dir(l.SourcePath)); err != nil {
			return err
		}
		if data, err := os.ReadFile(l.SourcePath); os.IsNotExist(err) {
			if err := writeAtomic(l.SourcePath, []byte(content)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if string(data) != content {
			return fmt.Errorf("分类待整理文件冲突")
		}
		m.STRMPath = l.SourcePath
		if err := s.Store.AdoptUploadedCopy(ctx, a.OriginalID, m, l); err != nil {
			return err
		}
		step.Item.Previous = &l
		step.Item.Link.RemoteID = m.RemoteID
		step.Item.Link.InboxID = a.OriginalID
		step.Item.Media = m
		step.Item.Media.STRMPath = step.Item.Link.OutputPath
		if err := set("mapped"); err != nil {
			return err
		}
	}
	if e.Status == "mapped" {
		// Old upload STRM shares the stable playback content; remove only the
		// project file after the new mapping has been committed.
		old := step.OldPath
		if old != step.Item.Link.SourcePath {
			if err := s.removeOwnedSTRM(ctx, c, old, s.mediaContent(c, step.Item.Media), step.Item.Media.RemoteID); err != nil {
				return err
			}
			removeEmptyLocalParents(filepath.Dir(old), c.PendingPath)
		}
		if err := set("completed"); err != nil {
			return err
		}
		s.Store.Audit(ctx, "library", fmt.Sprintf("上传分类归档 %s；接收原件 %s 保留，分类副本 %s，待整理 %s", a.Relative, a.OriginalID, step.Copy.ID, step.Item.Link.SourcePath))
	}
	return nil
}

// One upload file per recognition task: a shared staging label must never be
// used as the movie title or combine unrelated uploads into one match.
func (s *Service) queueUploadedLocal(ctx context.Context, m store.MediaEntry) error {
	// Queueing can be triggered by the upload callback, the periodic scanner,
	// and an explicit full local reconciliation at the same time. Keep the
	// existence check and insert together so one RemoteID receives one job.
	s.uploadQueueMu.Lock()
	defer s.uploadQueueMu.Unlock()
	jobs, err := s.Store.OrganizationJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Status == "completed" || j.Status == "cleaned" {
			continue
		}
		var ids []string
		_ = json.Unmarshal(j.Expected, &ids)
		for _, id := range ids {
			if id == m.RemoteID {
				return nil
			}
		}
	}
	id, err := randomID(12)
	if err != nil {
		return err
	}
	ids, _ := json.Marshal([]string{m.RemoteID})
	j := &store.TransferJob{ID: id, Source: "local", StageCID: filepath.ToSlash(filepath.Dir(strings.TrimSuffix(m.RemotePath, filepath.Ext(m.RemotePath)))), Title: strings.TrimSuffix(m.Name, filepath.Ext(m.Name)), Status: "queued", Expected: ids, CreatedAt: time.Now().UTC()}
	if err := s.Store.CreateJob(ctx, j); err != nil {
		return err
	}
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return err
	}
	s.Enqueue(id)
	return nil
}
