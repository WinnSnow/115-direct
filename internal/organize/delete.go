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

	"github.com/local/115-direct/internal/store"
)

type DeletePreview struct {
	RemoteID string   `json:"remote_id"`
	Policy   string   `json:"policy"`
	Output   string   `json:"output"`
	Source   string   `json:"source"`
	Cloud    string   `json:"cloud"`
	Retained []string `json:"retained"`
	Assets   []string `json:"assets"`
	Digest   string   `json:"digest"`
}

func (s *Service) previewDelete(ctx context.Context, c DirectoryConfig, id, policy string) (*DeletePreview, error) {
	if !validPolicy(policy) {
		return nil, fmt.Errorf("删除策略无效")
	}
	l, err := s.Store.MediaLink(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &DeletePreview{RemoteID: id, Policy: policy, Output: l.OutputPath, Retained: []string{}, Assets: []string{}}
	if policy != "output" {
		p.Source = l.SourcePath
	}
	if policy == "chain" {
		if l.InboxID == id {
			return nil, fmt.Errorf("接收原件始终保留")
		}
		p.Cloud = id
	}
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return nil, err
	}
	for _, other := range links {
		if other.RemoteID == id || other.Deleted != "" {
			continue
		}
		if p.Source != "" && (other.OutputPath == p.Source || (other.Mode == "symlink" && other.SourcePath == p.Source)) {
			p.Retained = append(p.Retained, "待整理文件被 "+other.RemoteID+" 引用")
		}
		if other.OutputPath == p.Output && p.Output != "" {
			p.Retained = append(p.Retained, "成品被 "+other.RemoteID+" 引用")
		}
	}
	assets, err := s.Store.Assets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		owned := false
		shared := false
		for _, owner := range a.Owners {
			if owner == id {
				owned = true
			} else {
				shared = true
			}
		}
		if owned {
			if shared {
				p.Retained = append(p.Retained, "共享元数据: "+a.Path)
			} else {
				p.Assets = append(p.Assets, a.Path)
			}
		}
	}
	raw, _ := json.Marshal(p)
	p.Digest = store.ContentHash(raw)
	return p, nil
}
func (s *Service) PreviewDelete(ctx context.Context, id string) (*DeletePreview, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.previewDelete(ctx, s.Directories(ctx), id, s.Options(ctx).DeletePolicy)
}
func (s *Service) Delete(ctx context.Context, id, digest string) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	c := s.Directories(ctx)
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return err
	}
	p, err := s.previewDelete(ctx, c, id, s.Options(ctx).DeletePolicy)
	if err != nil {
		return err
	}
	if digest == "" || digest != p.Digest {
		return fmt.Errorf("删除预览已变化")
	}
	operation, err := randomID(12)
	if err != nil {
		return err
	}
	return s.deleteMediaLocked(ctx, c, id, p.Policy, "delete:"+operation)
}

type deleteStep struct {
	Config  DirectoryConfig `json:"config"`
	Link    store.MediaLink `json:"link"`
	Content string          `json:"content"`
	Policy  string          `json:"policy"`
}

func (s *Service) deleteMediaLocked(ctx context.Context, c DirectoryConfig, id, policy, operation string) error {
	e, err := s.Store.Execution(ctx, operation)
	if errors.Is(err, sql.ErrNoRows) {
		l, err := s.Store.MediaLink(ctx, id)
		if err != nil {
			return err
		}
		m, err := s.Store.GetMediaByRemoteID(ctx, id)
		if err != nil {
			return err
		}
		if !validPolicy(policy) {
			return fmt.Errorf("删除策略无效")
		}
		raw, _ := json.Marshal(deleteStep{c, *l, s.mediaContent(c, *m), policy})
		e = &store.Execution{ID: operation, Kind: "delete", Status: "planned", Body: raw}
		if err := s.Store.PutExecution(ctx, *e); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return s.resumeDelete(ctx, e)
}
func (s *Service) resumeDelete(ctx context.Context, e *store.Execution) (result error) {
	if e.Status == "completed" {
		return nil
	}
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var d deleteStep
	if err := json.Unmarshal(e.Body, &d); err != nil {
		return err
	}
	if err := CheckRoots(ctx, s.Store, d.Config); err != nil {
		return err
	}
	set := func(status string) error { e.Status = status; e.Error = ""; return s.Store.PutExecution(ctx, *e) }
	if e.Status == "planned" {
		current, err := s.Store.MediaLink(ctx, d.Link.RemoteID)
		if err != nil {
			return err
		}
		current.Deleted = d.Policy
		if err := s.Store.PutLink(ctx, *current); err != nil {
			return err
		}
		if d.Link.OutputPath != "" {
			if err := s.removeOwnedSTRM(ctx, d.Config, d.Link.OutputPath, d.Content, d.Link.RemoteID); err != nil {
				return err
			}
		}
		for _, path := range d.Link.Copies {
			if err := s.removeOwnedSTRM(ctx, d.Config, path, d.Content, d.Link.RemoteID); err != nil {
				return err
			}
		}
		if err := s.releaseAssets(ctx, d.Config, d.Link.RemoteID, ""); err != nil {
			return err
		}
		if err := set("output_deleted"); err != nil {
			return err
		}
	}
	if e.Status == "output_deleted" {
		if d.Policy != "output" && d.Link.SourcePath != "" && d.Link.SourcePath != d.Link.OutputPath {
			if err := s.removeOwnedSTRM(ctx, d.Config, d.Link.SourcePath, d.Content, d.Link.RemoteID); err != nil {
				return err
			}
		}
		if err := set("local_deleted"); err != nil {
			return err
		}
	}
	if e.Status == "local_deleted" {
		if d.Policy == "chain" {
			if d.Link.RemoteID == d.Link.InboxID {
				return fmt.Errorf("目标属于接收原件，已保留")
			}
			if d.Config.LibraryCID == "" || d.Config.LibraryCID == d.Config.InboxCID {
				return fmt.Errorf("115 分类目录无效")
			}
			entries, err := s.walk(ctx, d.Config.LibraryCID, "")
			if err != nil {
				return err
			}
			found := false
			for _, f := range entries {
				if f.Entry.ID == d.Link.RemoteID {
					found = true
					break
				}
			}
			if found {
				inbox, err := s.walk(ctx, d.Config.InboxCID, "")
				if err != nil {
					return err
				}
				for _, f := range inbox {
					if f.Entry.ID == d.Link.RemoteID {
						return fmt.Errorf("目标属于接收原件，已保留")
					}
				}
				if err := s.Pan.Delete(ctx, d.Link.RemoteID); err != nil {
					return err
				}
			}
		}
		if err := set("completed"); err != nil {
			return err
		}
		_ = s.Store.ResolveReview(ctx, "local:"+d.Link.RemoteID, "confirmed")
		_ = s.Store.ResolveReview(ctx, "cloud:"+d.Link.RemoteID, "confirmed")
		s.Store.Audit(ctx, "file", fmt.Sprintf("明确删除 %s；策略 %s；接收原件保留；任务 %s", d.Link.RemoteID, d.Policy, e.ID))
	}
	return nil
}
func (s *Service) Restore(ctx context.Context, id string) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	l, err := s.Store.MediaLink(ctx, id)
	if err != nil {
		return err
	}
	steps, err := s.Store.Executions(ctx)
	if err != nil {
		return err
	}
	for _, e := range steps {
		if e.Kind == "delete" && e.Status != "completed" {
			var d deleteStep
			if json.Unmarshal(e.Body, &d) == nil && d.Link.RemoteID == id {
				return fmt.Errorf("删除执行仍待重试，请先处理任务")
			}
		}
	}
	if l.Deleted == "chain" {
		c := s.Directories(ctx)
		if c.LibraryCID == "" {
			return fmt.Errorf("分类目录未配置")
		}
		entries, err := s.walk(ctx, c.LibraryCID, "")
		if err != nil {
			return err
		}
		found := false
		for _, f := range entries {
			if f.Entry.ID == id {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("115分类源尚未恢复，请先恢复或明确重新接收")
		}
		l.Unavailable = false
	}
	if l.Deleted == "" && l.OutputPath != "" {
		if _, err := os.Lstat(l.OutputPath); err == nil {
			return fmt.Errorf("成品仍存在，请使用整理/纠错")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	l.Deleted = ""
	l.PreviousOutput = l.OutputPath
	l.OutputPath = ""
	l.Mode = ""
	l.Suppressed = ""
	if err := s.Store.PutLink(ctx, *l); err != nil {
		return err
	}
	_ = s.Store.ResolveReview(ctx, "local:"+id, "restored")
	s.Store.Audit(ctx, "file", "明确恢复自动整理: "+id)
	return nil
}
func (s *Service) RetryExecution(ctx context.Context, id string) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	e, err := s.Store.Execution(ctx, id)
	if err != nil {
		return err
	}
	switch e.Kind {
	case "upload_archive":
		return s.resumeUploadArchive(ctx, e)
	case "share_receive":
		if e.Status == "completed" {
			return nil
		}
		var step struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(e.Body, &step); err != nil {
			return err
		}
		if step.JobID == "" {
			return fmt.Errorf("分享接收执行记录缺少任务ID")
		}
		return s.Retry(ctx, step.JobID)
	case "organize":
		return s.retryOrganizationStep(ctx, e)
	case "delete":
		return s.resumeDelete(ctx, e)
	case "file":
		return s.resumeOperation(ctx, e)
	case "asset":
		return s.resumeAsset(ctx, e)
	case "image":
		return s.resumeImage(ctx, e)
	}
	return fmt.Errorf("执行类型无效")
}

func (s *Service) releaseAssets(ctx context.Context, c DirectoryConfig, id, keepOutput string) error {
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return err
	}
	assets, err := s.Store.Assets(ctx)
	if err != nil {
		return err
	}
	for _, a := range assets {
		owners := []string{}
		found := false
		for _, owner := range a.Owners {
			if owner == id {
				found = true
			} else {
				owners = append(owners, owner)
			}
		}
		if !found {
			continue
		}
		base := filepath.Base(a.Path)
		shared := base == "movie.nfo" || base == "tvshow.nfo" || base == "season.nfo" || base == "poster.jpg" || base == "fanart.jpg"
		if shared {
			for _, l := range links {
				if l.RemoteID == id || l.Deleted != "" || l.OutputPath == "" {
					continue
				}
				if filepath.Dir(a.Path) == filepath.Dir(l.OutputPath) || filepath.Dir(a.Path) == filepath.Dir(filepath.Dir(l.OutputPath)) {
					found := false
					for _, owner := range owners {
						if owner == l.RemoteID {
							found = true
						}
					}
					if !found {
						owners = append(owners, l.RemoteID)
					}
				}
			}
		}
		if keepOutput != "" {
			base := filepath.Base(a.Path)
			shared := base == "movie.nfo" || base == "tvshow.nfo" || base == "season.nfo" || base == "poster.jpg" || base == "fanart.jpg"
			if a.Path == strings.TrimSuffix(keepOutput, filepath.Ext(keepOutput))+".nfo" || (shared && (filepath.Dir(a.Path) == filepath.Dir(keepOutput) || filepath.Dir(a.Path) == filepath.Dir(filepath.Dir(keepOutput)))) {
				continue
			}
		}
		if len(owners) > 0 {
			a.Owners = owners
			if err := s.Store.PutAsset(ctx, a); err != nil {
				return err
			}
			continue
		}
		if !within(c.STRMPath, a.Path) {
			return fmt.Errorf("元数据路径越界")
		}
		if err := validateMetadataDir(c.STRMPath, filepath.Dir(a.Path)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		data, err := os.ReadFile(a.Path)
		if os.IsNotExist(err) {
			if err := s.Store.RemoveAsset(ctx, a.Path); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if store.ContentHash(data) != a.Hash {
			s.Store.Audit(ctx, "file", "用户修改的元数据已保留: "+a.Path)
			continue
		}
		if err := os.Remove(a.Path); err != nil {
			return err
		}
		if err := s.Store.RemoveAsset(ctx, a.Path); err != nil {
			return err
		}
	}
	return nil
}
