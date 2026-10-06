package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Only the public fields are projected. Execution bodies contain signed playback
// URLs and directory secrets and must never be returned by the records API.
type OrganizationRecord struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	FileID   string `json:"file_id"`
	RemoteID string `json:"remote_id"`
	InboxID  string `json:"inbox_id,omitempty"`
	Name     string `json:"name"`
	// CloudTargetPath is the path relative to the configured 115 library root.
	// CloudSourcePath and CloudOperation are filled by the HTTP projection because
	// resolving the receive tree requires the 115 provider.
	CloudTargetPath string    `json:"cloud_target_path,omitempty"`
	CloudSourcePath string    `json:"cloud_source_path,omitempty"`
	CloudOperation  string    `json:"cloud_operation,omitempty"`
	SourcePath      string    `json:"source_path"`
	OutputPath      string    `json:"output_path"`
	Mode            string    `json:"mode"`
	VersionGroup    string    `json:"version_group"`
	Kind            string    `json:"kind"`
	TMDBID          int64     `json:"tmdb_id"`
	Quality         Quality   `json:"quality"`
	Season          int       `json:"season"`
	Episode         int       `json:"episode"`
	EpisodeEnd      int       `json:"episode_end"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	RetryBlocked    string    `json:"retry_blocked,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type organizationItem struct {
	Media MediaEntry `json:"media"`
	Link  MediaLink  `json:"link"`
	Skip  string     `json:"skip"`
}
type organizationPlan struct {
	Items   []organizationItem `json:"items"`
	Request struct {
		IDs []string `json:"ids"`
	} `json:"request"`
}

func (s *Store) OrganizationJobs(ctx context.Context) ([]TransferJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM transfer_jobs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []TransferJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *j)
	}
	return jobs, rows.Err()
}

func (s *Store) OrganizationRecords(ctx context.Context, filter RecordFilter) ([]OrganizationRecord, int, error) {
	f := recordPage(filter)
	execs, err := s.listExecutions(ctx, `SELECT id,kind,status,body,error,updated_at FROM execution_steps WHERE kind='organize' ORDER BY updated_at DESC`)
	if err != nil {
		return nil, 0, err
	}
	links, err := s.ListLinks(ctx)
	if err != nil {
		return nil, 0, err
	}
	media, err := s.ListMedia(ctx)
	if err != nil {
		return nil, 0, err
	}
	jobs, err := s.OrganizationJobs(ctx)
	if err != nil {
		return nil, 0, err
	}
	byLink := map[string]MediaLink{}
	byMedia := map[string]MediaEntry{}
	byJob := map[string]TransferJob{}
	for _, l := range links {
		byLink[l.RemoteID] = l
	}
	for _, m := range media {
		byMedia[m.RemoteID] = m
	}
	for _, j := range jobs {
		byJob[j.ID] = j
	}
	seen := map[string]bool{}
	pairs := map[string]bool{}
	all := []OrganizationRecord{}
	makeRecord := func(id, task string, item organizationItem, updated time.Time) OrganizationRecord {
		m, l := item.Media, item.Link
		if m.ID == "" {
			m = byMedia[l.RemoteID]
		} else if latest, ok := byMedia[m.RemoteID]; ok {
			// Older execution bodies may predate the remote-path projection. Merge
			// the durable ledger value without replacing the execution's identity.
			if m.RemotePath == "" {
				m.RemotePath = latest.RemotePath
			}
			if m.Name == "" {
				m.Name = latest.Name
			}
		}
		if l.RemoteID == "" {
			l = byLink[m.RemoteID]
		}
		return OrganizationRecord{ID: id, TaskID: task, FileID: m.ID, RemoteID: l.RemoteID, InboxID: l.InboxID, Name: m.Name, CloudTargetPath: m.RemotePath, SourcePath: l.SourcePath, OutputPath: l.OutputPath, Mode: l.Mode, VersionGroup: l.VersionGroup, Kind: l.Kind, TMDBID: l.TMDBID, Quality: l.Quality, Season: l.Season, Episode: l.Episode, EpisodeEnd: l.EpisodeEnd, UpdatedAt: updated}
	}
	jobStatus := func(r *OrganizationRecord, j TransferJob) {
		switch j.Status {
		case "failed":
			r.Status = "failed"
			r.Error = Redact(j.Error)
		case "waiting_match":
			r.Status = "unrecognized"
			r.Error = Redact(j.Error)
			if j.TMDBID > 0 {
				r.Status = "needs_confirmation"
			}
		case "queued", "retry":
			r.Status = "queued"
		case "matching", "organizing", "verifying":
			r.Status = "running"
		}
		if j.UpdatedAt.After(r.UpdatedAt) {
			r.UpdatedAt = j.UpdatedAt
		}
	}
	for _, e := range execs {
		var b struct {
			Item organizationItem `json:"item"`
		}
		if err := json.Unmarshal(e.Body, &b); err != nil {
			return nil, 0, err
		}
		parts := strings.SplitN(e.ID, ":", 3)
		if len(parts) != 3 {
			continue
		}
		r := makeRecord(e.ID, parts[1], b.Item, e.UpdatedAt)
		r.Status = "running"
		if e.Status == "completed" {
			r.Status = "success"
		}
		if j, ok := byJob[r.TaskID]; ok {
			if j.Status != "failed" || !e.UpdatedAt.After(j.UpdatedAt) || e.Status != "completed" {
				jobStatus(&r, j)
			}
		}
		if e.Error != "" {
			r.Status = "failed"
			r.Error = Redact(e.Error)
		}
		seen[r.RemoteID] = true
		pairs[r.TaskID+":"+r.RemoteID] = true
		all = append(all, r)
	}
	// Files awaiting recognition or skipped by the version policy may never have an
	// execution step. Their durable job selection/plan still yields one record/file.
	for _, j := range jobs {
		var p organizationPlan
		if j.Source == "manual" {
			if len(j.Actual) > 0 {
				if err := json.Unmarshal(j.Actual, &p); err != nil {
					return nil, 0, err
				}
			}
		} else {
			err := s.GetSetting(ctx, "organize_plan:"+j.ID, &p)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, 0, err
			}
		}
		if len(p.Items) == 0 && j.Source == "local" {
			var ids []string
			if len(j.Expected) > 0 {
				if err := json.Unmarshal(j.Expected, &ids); err != nil {
					return nil, 0, err
				}
			}
			if len(ids) == 0 && j.StageCID != "" {
				for _, l := range links {
					if strings.Contains(l.SourcePath, "/"+j.StageCID+"/") {
						ids = append(ids, l.RemoteID)
					}
				}
			}
			for _, id := range ids {
				p.Items = append(p.Items, organizationItem{Media: byMedia[id], Link: byLink[id]})
			}
		}
		for _, item := range p.Items {
			id := item.Link.RemoteID
			if id == "" {
				id = item.Media.RemoteID
			}
			if id == "" || pairs[j.ID+":"+id] {
				continue
			}
			r := makeRecord("job:"+j.ID+":"+id, j.ID, item, j.UpdatedAt)
			r.Status = "success"
			jobStatus(&r, j)
			if item.Skip != "" && item.Skip != "已完成相同整理" {
				r.Status = "skipped"
				r.Error = item.Skip
			}
			pairs[j.ID+":"+id] = true
			seen[id] = true
			all = append(all, r)
		}
	}
	for _, l := range links {
		if seen[l.RemoteID] || l.Deleted != "" {
			continue
		}
		// Older cloud organize runs did not persist an organize plan or
		// execution step.  The durable link still carries the receive directory
		// CID, which is the stable bridge back to the transfer job.  Project it
		// to that job even after the output STRM has been published; previously
		// this association was only made for links without an output path.
		taskID := ""
		recordID := "media:" + l.RemoteID
		if l.InboxID != "" {
			for _, j := range jobs {
				if j.StageCID == l.InboxID && j.Source != "local" && j.Source != "manual" {
					taskID = j.ID
					recordID = "job:" + j.ID + ":" + l.RemoteID
					break
				}
			}
		}
		r := makeRecord(recordID, taskID, organizationItem{Media: byMedia[l.RemoteID], Link: l}, l.IngestedAt)
		r.Status = "unrecognized"
		if l.OutputPath != "" {
			r.Status = "success"
		}
		if l.Suppressed != "" {
			r.Status = "skipped"
			r.Error = l.Suppressed
		}
		if l.OutputPath == "" && l.Suppressed == "" {
			for _, j := range jobs {
				if j.StageCID != "" && l.InboxID == j.StageCID {
					r.TaskID = j.ID
					jobStatus(&r, j)
					break
				}
			}
		}
		if r.TaskID != "" {
			pairs[r.TaskID+":"+r.RemoteID] = true
		}
		all = append(all, r)
	}
	// A legacy cloud task can share a media link with a later local organize
	// execution.  The global history deliberately de-duplicates that media
	// item, but a task preview still needs a task-scoped projection.  When the
	// caller searches a specific task, add an alias through the durable
	// InboxID/StageCID bridge without polluting the global history list.
	searchTerm := strings.ToLower(strings.TrimSpace(f.Search))
	if searchTerm != "" {
		for _, j := range jobs {
			if !strings.Contains(strings.ToLower(j.ID), searchTerm) || j.StageCID == "" || j.Source == "local" || j.Source == "manual" {
				continue
			}
			for _, l := range links {
				if l.Deleted != "" || l.InboxID != j.StageCID || pairs[j.ID+":"+l.RemoteID] {
					continue
				}
				r := makeRecord("job:"+j.ID+":"+l.RemoteID, j.ID, organizationItem{Media: byMedia[l.RemoteID], Link: l}, l.IngestedAt)
				r.Status = "unrecognized"
				if l.OutputPath != "" {
					r.Status = "success"
				}
				if l.Suppressed != "" {
					r.Status = "skipped"
					r.Error = l.Suppressed
				}
				if l.OutputPath == "" && l.Suppressed == "" {
					jobStatus(&r, j)
				}
				pairs[j.ID+":"+l.RemoteID] = true
				all = append(all, r)
			}
		}
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	filtered := []OrganizationRecord{}
	q := searchTerm
	for _, r := range all {
		l := byLink[r.RemoteID]
		if l.Deleted != "" {
			r.RetryBlocked = "文件已删除，请先明确恢复"
		} else if l.Unavailable {
			r.RetryBlocked = "云端源不可用，请先确认源文件"
		}
		if r.Status == "running" || r.Status == "queued" {
			r.RetryBlocked = "整理任务正在执行或等待执行"
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.SourcePath+" "+r.OutputPath+" "+r.FileID+" "+r.RemoteID+" "+r.ID+" "+r.VersionGroup), q) {
			continue
		}
		filtered = append(filtered, r)
	}
	total := len(filtered)
	if f.Offset > total {
		f.Offset = total
	}
	end := f.Offset + f.Limit
	if end > total {
		end = total
	}
	return filtered[f.Offset:end], total, nil
}
