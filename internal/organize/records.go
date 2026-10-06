package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/115-direct/internal/store"
)

// ReorganizeRecord queues one file, using current settings and its confirmed
// identity. Failed partial steps are resumed rather than published twice.
func (s *Service) ReorganizeRecord(ctx context.Context, id string) (*store.TransferJob, error) {
	return s.reorganizeRecord(ctx, id, "", 0)
}

// ReorganizeRecordWithMatch queues one file with an explicitly selected TMDB
// identity. The identity is used to build a fresh plan and is persisted when
// the queued organization job is processed.
func (s *Service) ReorganizeRecordWithMatch(ctx context.Context, id, kind string, tmdbID int64) (*store.TransferJob, error) {
	if kind != "movie" && kind != "tv" {
		return nil, fmt.Errorf("kind must be movie or tv")
	}
	if tmdbID <= 0 {
		return nil, fmt.Errorf("tmdb id must be positive")
	}
	return s.reorganizeRecord(ctx, id, kind, tmdbID)
}

func (s *Service) reorganizeRecord(ctx context.Context, id, kindOverride string, tmdbIDOverride int64) (*store.TransferJob, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	remoteID, taskID := "", ""
	var step *store.Execution
	switch {
	case strings.HasPrefix(id, "organize:"):
		e, err := s.Store.Execution(ctx, id)
		if err != nil {
			return nil, err
		}
		step = e
		var b fileStep
		if err := json.Unmarshal(e.Body, &b); err != nil {
			return nil, err
		}
		remoteID = b.Item.Media.RemoteID
	case strings.HasPrefix(id, "media:"):
		remoteID = strings.TrimPrefix(id, "media:")
	case strings.HasPrefix(id, "job:"):
		p := strings.SplitN(id, ":", 3)
		if len(p) != 3 {
			return nil, fmt.Errorf("整理记录无效")
		}
		taskID, remoteID = p[1], p[2]
	default:
		return nil, fmt.Errorf("整理记录无效")
	}
	l, err := s.Store.MediaLink(ctx, remoteID)
	if err != nil {
		return nil, err
	}
	if l.Deleted != "" || l.Unavailable {
		return nil, fmt.Errorf("文件已删除或源不可用，请先处理目录关联")
	}
	jobs, err := s.Store.OrganizationJobs(ctx)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Source != "local" && j.Source != "manual" {
			continue
		}
		if j.Status != "queued" && j.Status != "matching" && j.Status != "organizing" && j.Status != "verifying" {
			continue
		}
		var ids []string
		_ = json.Unmarshal(j.Expected, &ids)
		if j.Source == "manual" {
			var p OrganizePlan
			_ = json.Unmarshal(j.Actual, &p)
			ids = p.Request.IDs
		}
		for _, selected := range ids {
			if selected == remoteID {
				return nil, fmt.Errorf("该文件已有整理任务等待执行")
			}
		}
	}
	if step != nil && step.Status != "completed" {
		if err := s.retryOrganizationStep(ctx, step); err != nil {
			return nil, err
		}
		return nil, nil
	}
	kind, tmdbID := l.Kind, l.TMDBID
	if kindOverride != "" {
		kind = kindOverride
		tmdbID = tmdbIDOverride
	}
	if tmdbID == 0 && taskID != "" {
		j, err := s.Store.GetJob(ctx, taskID)
		if err != nil {
			return nil, err
		}
		kind, tmdbID = j.TMDBKind, j.TMDBID
	}
	var p *OrganizePlan
	if tmdbID > 0 {
		d, err := s.TMDB.Details(ctx, kind, tmdbID)
		if err != nil {
			return nil, err
		}
		req := OrganizeRequest{IDs: []string{remoteID}, Kind: kind, TMDBID: tmdbID, Manual: true}
		if l.Kind == "tv" && l.Episode > 0 {
			req.Season = &l.Season
			req.AllowSpecial = l.Season == 0
			if l.EpisodeEnd == l.Episode {
				req.Episode = &l.Episode
			} else {
				req.AllowMulti = true
			}
		}
		plan, err := s.buildPlan(ctx, s.Directories(ctx), req, d)
		if err != nil {
			return nil, err
		}
		p = plan
	}
	jid, err := randomID(12)
	if err != nil {
		return nil, err
	}
	j := &store.TransferJob{ID: jid, Source: "local", Status: "queued", TMDBKind: kind, TMDBID: tmdbID, Title: filepath.Base(l.SourcePath)}
	j.Expected, _ = json.Marshal([]string{remoteID})
	if p != nil {
		j.Source = "manual"
		j.Title = p.Details.Title
		j.Actual, _ = json.Marshal(p)
	} else {
		rel, err := filepath.Rel(s.Directories(ctx).PendingPath, filepath.Dir(l.SourcePath))
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("待整理路径无效")
		}
		j.StageCID = filepath.ToSlash(rel)
	}
	if err := s.Store.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	s.Store.Audit(ctx, "organize", "单文件重新整理已排队："+remoteID+"，任务 "+jid)
	s.Enqueue(jid)
	return j, nil
}

// Called under the library lock. The durable status is not rewound: publication,
// version retirement and metadata writes must retain their idempotent checkpoints.
func (s *Service) retryOrganizationStep(ctx context.Context, e *store.Execution) (result error) {
	defer func() {
		if result != nil {
			e.Error = result.Error()
		}
		_ = s.Store.PutExecution(context.Background(), *e)
	}()
	var b fileStep
	if err := json.Unmarshal(e.Body, &b); err != nil {
		return err
	}
	l, err := s.Store.MediaLink(ctx, b.Item.Media.RemoteID)
	if err != nil {
		return err
	}
	if l.Deleted != "" || l.Unavailable {
		return fmt.Errorf("文件已删除或源不可用，请先处理关联")
	}
	if (e.Status == "mapped" || e.Status == "completed") && (l.OutputPath != b.Item.Link.OutputPath || l.VersionGroup != b.Item.Link.VersionGroup) {
		return fmt.Errorf("关联已变化，请重新预览整理")
	}
	if e.Status == "published" && l.OutputPath != b.Item.Link.OutputPath && (b.Item.Previous == nil || l.OutputPath != b.Item.Previous.OutputPath) {
		return fmt.Errorf("关联已变化，请重新预览整理")
	}
	if err := CheckRoots(ctx, s.Store, b.Config); err != nil {
		return err
	}
	if e.Status != "planned" {
		data, err := os.ReadFile(b.Item.Link.OutputPath)
		if err != nil || string(data) != b.Content {
			return fmt.Errorf("成品缺失或已变化，请先处理目录关联")
		}
	}
	if err := s.resumeFile(ctx, e); err != nil {
		return err
	}
	d, err := s.TMDB.Details(ctx, b.Item.Link.Kind, b.Item.Link.TMDBID)
	if err != nil {
		return err
	}
	if err := s.writeMetadata(ctx, b.Config, d, []store.MediaEntry{b.Item.Media}); err != nil {
		return err
	}
	if err := s.refreshLibrary(ctx); err != nil {
		return err
	}
	e.Error = ""
	return nil
}

func (s *Service) refreshLibrary(ctx context.Context) error {
	if s.Refresh == nil {
		return nil
	}
	if err := s.Refresh(ctx); err != nil {
		s.Store.Log(ctx, "jellyfin", "warning", "媒体库更新通知失败："+err.Error())
		// Jellyfin is a notification target.  The STRM, metadata and durable
		// organization record have already been written when this hook runs, so
		// an unavailable Jellyfin endpoint must not turn a successful organize
		// operation into a failed one.  Keep the error in the audit/log stream so
		// it can be corrected and retried independently.
		s.Store.Audit(ctx, "jellyfin", "媒体库更新通知失败，整理结果已保留："+err.Error())
		return nil
	}
	s.Store.Audit(ctx, "jellyfin", "已通知 Jellyfin 更新媒体库")
	return nil
}
