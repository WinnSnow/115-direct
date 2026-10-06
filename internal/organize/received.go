package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type ReceivedOrganizeResult struct {
	Mode              string `json:"mode"`
	Queued            int    `json:"queued"`
	Requeued          int    `json:"requeued"`
	Skipped           int    `json:"skipped"`
	NeedsConfirmation int    `json:"needs_confirmation"`
}

type ReceivedCleanupResult struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days"`
	Removed int  `json:"removed"`
	Skipped int  `json:"skipped"`
}

// QueueReceived puts unclassified receive folders through the durable organizer.
func (s *Service) QueueReceived(ctx context.Context) (*ReceivedOrganizeResult, error) {
	return s.QueueReceivedMode(ctx, false)
}

// QueueReceivedFull performs a complete receive-directory pass. Completed
// cloud jobs are re-queued for reconciliation; the worker's durable-link check
// prevents a second receive/copy when the existing organization is intact.
func (s *Service) QueueReceivedFull(ctx context.Context) (*ReceivedOrganizeResult, error) {
	return s.QueueReceivedMode(ctx, true)
}

func (s *Service) QueueReceivedMode(ctx context.Context, full bool) (*ReceivedOrganizeResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	cfg := s.Directories(ctx)
	if cfg.InboxCID == "" || cfg.LibraryCID == "" {
		return nil, fmt.Errorf("接收目录和整理目录尚未配置")
	}
	entries, err := s.Pan.List(ctx, cfg.InboxCID)
	if err != nil {
		return nil, err
	}
	jobs, err := s.Store.ListJobs(ctx, 200)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	completed := map[string]store.TransferJob{}
	for _, job := range jobs {
		if job.Status == "completed" && job.StageCID != "" {
			completed[job.StageCID] = job
		}
		if job.StageCID != "" && job.Status != "completed" && job.Status != "failed" && job.Status != "waiting_match" {
			active[job.StageCID] = true
		}
	}
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return nil, err
	}
	linked := map[string]bool{}
	for _, link := range links {
		linked[link.RemoteID] = true
	}
	result := &ReceivedOrganizeResult{Mode: "incremental"}
	if full {
		result.Mode = "full"
	}
	for _, entry := range entries {
		if !entry.Directory {
			continue
		}
		if active[entry.ID] || strings.Contains(entry.Name, "[接收中-") {
			result.Skipped++
			continue
		}
		if completedJob, ok := completed[entry.ID]; ok {
			if full && completedJob.Source != "local" && completedJob.Source != "manual" && completedJob.Source != "scrape" {
				completedJob.Status = "retry"
				completedJob.Error = ""
				if err := s.Store.UpdateJob(ctx, &completedJob); err != nil {
					return nil, err
				}
				s.Enqueue(completedJob.ID)
				result.Requeued++
			} else {
				result.Skipped++
			}
			continue
		}
		hint := tmdb.ExtractHint(entry.Name)
		if hint.TMDBID == 0 {
			result.Skipped++
			continue
		}
		files, err := s.walk(ctx, entry.ID, "")
		if err != nil {
			return nil, err
		}
		videoCount, mappedCount := 0, 0
		for _, file := range files {
			if classify(file.Entry.Name) != Video {
				continue
			}
			videoCount++
			if linked[file.Entry.ID] {
				mappedCount++
			}
		}
		if videoCount == 0 || mappedCount == videoCount {
			result.Skipped++
			continue
		}
		if mappedCount > 0 {
			result.NeedsConfirmation++
			continue
		}
		details, err := s.detailsByID(ctx, hint.TMDBID, hint.Kind)
		if err != nil {
			result.NeedsConfirmation++
			continue
		}
		id, err := randomID(12)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(files)
		job := &store.TransferJob{ID: id, Source: "inbox", Sender: "手动整理接收目录", Status: "queued", StageCID: entry.ID, TMDBKind: details.Kind, TMDBID: details.ID, Title: details.Title, Expected: raw, Actual: raw}
		if err := s.Store.CreateJob(ctx, job); err != nil {
			return nil, err
		}
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return nil, err
		}
		s.Store.Audit(ctx, "receive", "手动整理接收目录："+entry.Name)
		s.Enqueue(id)
		result.Queued++
	}
	return result, nil
}

// CleanupReceived removes only completed, mapped folders still directly under inbox.
func (s *Service) CleanupReceived(ctx context.Context) (*ReceivedCleanupResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	o := s.Options(ctx)
	result := &ReceivedCleanupResult{Enabled: o.ReceiveCleanupMode == "after_days", Days: o.ReceiveCleanupDays}
	if !result.Enabled {
		return result, nil
	}
	cfg := s.Directories(ctx)
	if cfg.InboxCID == "" {
		return nil, fmt.Errorf("接收目录尚未配置")
	}
	children, err := s.Pan.List(ctx, cfg.InboxCID)
	if err != nil {
		return nil, err
	}
	childByID := map[string]pan115.Entry{}
	for _, child := range children {
		childByID[child.ID] = child
	}
	jobs, err := s.Store.ListJobs(ctx, 200)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(o.ReceiveCleanupDays) * 24 * time.Hour)
	for _, job := range jobs {
		if job.Status != "completed" || job.StageCID == "" || job.CreatedAt.After(cutoff) || job.Source == "local" || job.Source == "scrape" {
			continue
		}
		entry, ok := childByID[job.StageCID]
		if !ok || !entry.Directory || strings.Contains(entry.Name, "[接收中-") {
			result.Skipped++
			continue
		}
		var saved placement
		if err := s.Store.GetSetting(ctx, "placement:"+job.ID, &saved); err != nil || saved.CID == "" {
			result.Skipped++
			continue
		}
		copied, err := s.walk(ctx, saved.CID, "")
		if err != nil || len(placementInventory(copied)) == 0 {
			result.Skipped++
			continue
		}
		if err := s.Pan.Delete(ctx, entry.ID); err != nil {
			return nil, err
		}
		result.Removed++
		s.Store.Audit(ctx, "receive", fmt.Sprintf("自动清理接收目录：%s；保留分类副本和本地关联", entry.Name))
	}
	return result, nil
}

func (s *Service) receivedCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.CleanupReceived(ctx); err != nil {
				slog.Warn("received directory cleanup failed", "error", err)
			}
		}
	}
}
