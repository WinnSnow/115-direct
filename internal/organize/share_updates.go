package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type ShareSubmissionOptions struct {
	CheckUpdates  bool
	CheckInterval time.Duration
	Force         bool
}
type shareSelection struct {
	Snapshot  pan115.ShareSnapshot `json:"snapshot"`
	Selected  []pan115.ShareFile   `json:"selected,omitempty"`
	Update    bool                 `json:"update"`
	CheckedAt time.Time            `json:"checked_at"`
}
type shareCheck struct {
	At time.Time `json:"at"`
}

func shareFingerprint(files []pan115.ShareFile) string {
	type identity struct {
		Identity string
		Size     int64
	}
	rows := make([]identity, 0, len(files))
	for _, f := range files {
		rows = append(rows, identity{shareFileIdentity(f), f.Entry.Size})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Identity < rows[j].Identity })
	raw, _ := json.Marshal(rows)
	return store.ContentHash(raw)
}

// shareFileIdentity intentionally ignores parent directories. Share providers
// commonly regenerate a share with a new outer folder name while retaining
// the same video files. Content hashes are preferred; when a provider omits
// SHA1, a normalized basename plus size is the stable local identity.
func shareFileIdentity(f pan115.ShareFile) string {
	name := normalizeShareFilename(path.Base(f.Relative))
	sha := strings.ToUpper(strings.TrimSpace(f.Entry.SHA1))
	if sha != "" {
		return fmt.Sprintf("sha1:%s:%d", sha, f.Entry.Size)
	}
	if name != "" {
		return fmt.Sprintf("name:%s:%d", name, f.Entry.Size)
	}
	if f.Entry.ID != "" {
		return "id:" + f.Entry.ID
	}
	return fmt.Sprintf("size:%d", f.Entry.Size)
}

func normalizeShareFilename(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.Join(strings.Fields(name), " ")
	return name
}

func verifyReceivedManifest(expected []pan115.ShareFile, actual []sourceFile) error {
	byPath := map[string]pan115.Entry{}
	for _, f := range actual {
		byPath[f.Relative] = f.Entry
	}
	if len(expected) != len(actual) {
		return fmt.Errorf("接收文件清单与提交时的分享内容不一致，请核对任务；不会重复转存")
	}
	for _, f := range expected {
		a, ok := byPath[f.Relative]
		if !ok || a.Size != f.Entry.Size || (f.Entry.SHA1 != "" && !strings.EqualFold(a.SHA1, f.Entry.SHA1)) {
			return fmt.Errorf("接收文件核对失败：%s；分享内容可能已变化", f.Relative)
		}
	}
	return nil
}

func changedShareFiles(old, current []pan115.ShareFile) []pan115.ShareFile {
	matched := make([]bool, len(old))
	out := []pan115.ShareFile{}
	for _, f := range current {
		found := -1
		for i := range old {
			if !matched[i] && sameShareFile(old[i], f) {
				found = i
				break
			}
		}
		if found < 0 {
			out = append(out, f)
		} else {
			matched[found] = true
		}
	}
	return out
}

func sameShareFile(old, current pan115.ShareFile) bool {
	if old.Entry.Size != current.Entry.Size {
		return false
	}
	if old.Entry.SHA1 != "" && current.Entry.SHA1 != "" {
		return strings.EqualFold(old.Entry.SHA1, current.Entry.SHA1)
	}
	// A provider may add/remove SHA1 metadata between snapshots. Matching the
	// normalized basename and size keeps that transition from re-receiving an
	// unchanged file, while differing hashes above still detect real changes.
	oldName := normalizeShareFilename(path.Base(old.Relative))
	currentName := normalizeShareFilename(path.Base(current.Relative))
	if oldName != "" && oldName == currentName {
		return true
	}
	return old.Entry.ID != "" && old.Entry.ID == current.Entry.ID
}

type shareBaselineMatch struct {
	Job       store.TransferJob
	Baseline  shareSelection
	Selected  []pan115.ShareFile
	Unchanged int
}

// findShareBaseline links a regenerated share URL to a completed share with
// the same confirmed TMDB identity. It requires a high overlap and that all
// historical files still exist in the current snapshot; removals and broad
// version changes remain separate/manual cases.
func (s *Service) findShareBaseline(ctx context.Context, kind string, tmdbID int64, current []pan115.ShareFile) (*shareBaselineMatch, error) {
	jobs, err := s.Store.CompletedShareJobs(ctx, kind, tmdbID, 50)
	if err != nil {
		return nil, err
	}
	var best *shareBaselineMatch
	for _, job := range jobs {
		if store.ShareKey(job.ShareURL) == "" {
			continue
		}
		var baseline shareSelection
		if err := s.Store.GetSetting(ctx, "share_selection:"+job.ID, &baseline); err != nil || len(baseline.Snapshot.Files) == 0 {
			continue
		}
		unchanged := 0
		matched := make([]bool, len(current))
		allHistoricalPresent := true
		for _, old := range baseline.Snapshot.Files {
			found := -1
			for i, file := range current {
				if !matched[i] && sameShareFile(old, file) {
					found = i
					break
				}
			}
			if found < 0 {
				allHistoricalPresent = false
				break
			}
			matched[found] = true
			unchanged++
		}
		if !allHistoricalPresent || unchanged == 0 || len(current) < len(baseline.Snapshot.Files) {
			continue
		}
		// A single-file share is safe only when the complete historical file
		// remains identical. Larger shows require at least 80% overlap.
		if len(baseline.Snapshot.Files) > 1 && unchanged*100 < len(baseline.Snapshot.Files)*80 {
			continue
		}
		selected := changedShareFiles(baseline.Snapshot.Files, current)
		candidate := &shareBaselineMatch{Job: job, Baseline: baseline, Selected: selected, Unchanged: unchanged}
		if best == nil || candidate.Unchanged > best.Unchanged || (candidate.Unchanged == best.Unchanged && candidate.Job.UpdatedAt.After(best.Job.UpdatedAt)) {
			best = candidate
		}
	}
	return best, nil
}

func duplicateShareMessage(j *store.TransferJob) string {
	switch j.Status {
	case "completed", "cleaned":
		return "已转存，无需重复发送。"
	case "failed":
		return "这个分享已有失败任务，请在转存记录中重试；不会重复创建任务。"
	case "waiting_match":
		return "这个分享已接收，等待人工识别；不会重复转存。"
	default:
		return "这个分享正在处理，无需重复发送。"
	}
}

// No polling: checks run only when a user submits the share again.
func (s *Service) SubmitShareMessage(ctx context.Context, sender, link, code, kind string, id int64, o ShareSubmissionOptions) (*store.TransferJob, error) {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	old, err := s.Store.ShareJob(ctx, link)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	duplicate := func(message string) (*store.TransferJob, error) {
		old.Duplicate = true
		old.SubmissionMessage = message
		return old, nil
	}
	if old != nil {
		if !o.CheckUpdates && !o.Force || old.Status != "completed" && old.Status != "cleaned" {
			return duplicate(duplicateShareMessage(old))
		}
		var check shareCheck
		if err := s.Store.GetSetting(ctx, "share_check:"+store.ShareKey(link), &check); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if !o.Force && o.CheckInterval > 0 && time.Since(check.At) < o.CheckInterval {
			return duplicate(fmt.Sprintf("已转存，无需重复发送。检查间隔内不重复核对；可发送“检查更新 %s”立即核对分享内容。", link))
		}
		if code == "" {
			code = old.ShareCode
			if code == "" {
				if parsed, err := pan115.ParseShareMessage(old.ShareURL); err == nil {
					code = parsed.Code
				}
			}
		}
	}
	provider, ok := s.Pan.(pan115.ShareTreeProvider)
	if !ok {
		return nil, fmt.Errorf("当前115接口不支持分享目录内容检查")
	}
	snapshot, err := provider.SnapshotShareTree(ctx, link, code)
	if err != nil {
		return nil, fmt.Errorf("分享内容检查失败，本次未转存：%w", err)
	}
	if snapshot == nil {
		return nil, fmt.Errorf("分享内容检查返回空结果")
	}
	if code == "" {
		code = snapshot.ReceiveCode
	}
	selection := shareSelection{Snapshot: *snapshot, CheckedAt: time.Now().UTC()}
	videoCount := 0
	for _, f := range snapshot.Files {
		if classify(f.Entry.Name) == Video {
			videoCount++
		}
	}
	if old == nil && videoCount == 0 {
		return nil, fmt.Errorf("分享中未找到可整理的视频文件，本次未转存")
	}
	previous := ""
	if old != nil {
		previous = old.ID
		var baseline shareSelection
		if err := s.Store.GetSetting(ctx, "share_selection:"+old.ID, &baseline); errors.Is(err, sql.ErrNoRows) {
			if old.StageCID == "" {
				return nil, fmt.Errorf("旧任务缺少历史文件清单与接收目录，请先核对旧转存记录")
			}
			files, err := s.walk(ctx, old.StageCID, "")
			if err != nil {
				return nil, fmt.Errorf("旧接收原件读取失败，未重复转存：%w", err)
			}
			for _, f := range files {
				baseline.Snapshot.Files = append(baseline.Snapshot.Files, pan115.ShareFile{Relative: f.Relative, Entry: f.Entry})
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("旧接收原件已清理且无历史文件清单，请先人工核对")
			}
			// Persist historical contents, never initialize the baseline with today's updated share.
			if err := s.Store.PutSetting(ctx, "share_selection:"+old.ID, baseline); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		selection.Update = true
		selection.Selected = changedShareFiles(baseline.Snapshot.Files, snapshot.Files)
		videos := 0
		for _, f := range selection.Selected {
			if classify(f.Entry.Name) == Video {
				videos++
			}
		}
		if videos == 0 {
			if err := s.Store.PutSetting(ctx, "share_check:"+store.ShareKey(link), shareCheck{selection.CheckedAt}); err != nil {
				return nil, err
			}
			s.Store.Audit(ctx, "transfer", "重复分享内容检查：无新增或变更视频；保留本地及云端已有文件 · "+old.ID)
			return duplicate("已转存，分享没有新增或变更的视频，无需重复发送。")
		}
		if kind == "" {
			kind = old.TMDBKind
		}
		if id == 0 {
			id = old.TMDBID
		}
	} else if kind != "" && id > 0 {
		// The share URL/code can change while the published show remains the
		// same. Link only a high-overlap, completed baseline; otherwise this is
		// intentionally treated as a new share.
		match, err := s.findShareBaseline(ctx, kind, id, snapshot.Files)
		if err != nil {
			return nil, err
		}
		if match != nil {
			// Treat the matched completed job as the duplicate target for the
			// no-change case; the incoming share code is still recorded when
			// there are new files to receive.
			old = &match.Job
			previous = match.Job.ID
			selection.Update = true
			selection.Selected = match.Selected
			if len(selection.Selected) == 0 {
				_ = s.Store.PutSetting(ctx, "share_check:"+store.ShareKey(link), shareCheck{selection.CheckedAt})
				return duplicate("已转存，分享内容没有新增或变更，无需重复发送。")
			}
			// Keep the existing TMDB identity when the new message omitted it.
			if kind == "" {
				kind = match.Job.TMDBKind
			}
			if id == 0 {
				id = match.Job.TMDBID
			}
		}
	}
	jobID, err := randomID(12)
	if err != nil {
		return nil, err
	}
	expected, _ := json.Marshal(snapshot.Entries)
	job := &store.TransferJob{ID: jobID, Source: "wecom", Sender: sender, ShareURL: link, ShareCode: code, Status: "queued", Title: snapshot.Title, Expected: expected, TMDBKind: kind, TMDBID: id}
	job, err = s.Store.CreateShareRevision(ctx, job, shareFingerprint(snapshot.Files), previous, selection)
	if err != nil {
		return nil, err
	}
	if job.Duplicate {
		job.SubmissionMessage = duplicateShareMessage(job)
		return job, nil
	}
	job.ShareUpdate = selection.Update
	if selection.Update {
		job.SubmissionMessage = fmt.Sprintf("检测到分享更新，已创建补充转存任务：%s\n只接收新增或变更文件，原有文件保留。", job.ID)
	} else {
		job.SubmissionMessage = "已创建转存任务：" + job.ID + "\n收到文件并完成核验后会再次通知。"
	}
	_ = s.Store.PutSetting(ctx, "share_check:"+store.ShareKey(link), shareCheck{selection.CheckedAt})
	s.Store.Audit(ctx, "transfer", fmt.Sprintf("企业微信分享接收任务 %s · 更新=%t · 文件清单=%d · 本次新增或变更=%d", job.ID, selection.Update, len(snapshot.Files), len(selection.Selected)))
	s.Enqueue(job.ID)
	return job, nil
}

// Build relative folders and persist each receive attempt before calling 115.
// An uncertain result is verified on retry; it is never submitted twice.
func (s *Service) receiveShareUpdate(ctx context.Context, j *store.TransferJob, selection shareSelection) error {
	groups := map[string][]pan115.ShareFile{}
	for _, f := range selection.Selected {
		if f.Relative == "" || path.IsAbs(f.Relative) || path.Clean(f.Relative) != f.Relative || strings.HasPrefix(f.Relative, "../") || strings.ContainsAny(f.Relative, "\\\x00") {
			return fmt.Errorf("分享更新路径无效")
		}
		groups[path.Dir(f.Relative)] = append(groups[path.Dir(f.Relative)], f)
	}
	paths := []string{}
	for rel := range groups {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		target := j.StageCID
		if rel != "." {
			for _, name := range strings.Split(rel, "/") {
				var err error
				target, err = s.ensureDir(ctx, target, name)
				if err != nil {
					return err
				}
			}
		}
		key := "share_receive:" + j.ID + ":" + store.ContentHash([]byte(rel))
		e, err := s.Store.Execution(ctx, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && e.Status == "completed" {
			continue
		}
		if err == nil {
			var recorded struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(e.Body, &recorded); err != nil {
				return err
			}
			if recorded.Target != target {
				return fmt.Errorf("补充转存目标目录已变化，请先核对执行记录")
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			raw, _ := json.Marshal(map[string]any{"job_id": j.ID, "relative": rel, "target": target, "files": groups[rel]})
			e = &store.Execution{ID: key, Kind: "share_receive", Status: "pending", Body: raw}
			if err := s.Store.PutExecution(ctx, *e); err != nil {
				return err
			}
			snap := &pan115.ShareSnapshot{Code: selection.Snapshot.Code, ReceiveCode: j.ShareCode}
			for _, f := range groups[rel] {
				snap.Entries = append(snap.Entries, f.Entry)
			}
			if err := s.Pan.ReceiveShare(ctx, snap, j.ShareCode, target); err != nil {
				e.Error = store.Redact(err.Error())
				_ = s.Store.PutExecution(ctx, *e)
				return err
			}
		}
		if err := s.waitShareGroup(ctx, target, groups[rel]); err != nil {
			return err
		}
		e.Status = "completed"
		e.Error = ""
		if err := s.Store.PutExecution(ctx, *e); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) waitShareGroup(ctx context.Context, target string, files []pan115.ShareFile) error {
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		actual, err := s.Pan.List(ctx, target)
		if err != nil {
			return err
		}
		missing := ""
		for _, f := range files {
			found := false
			for _, a := range actual {
				if !a.Directory && a.Name == f.Entry.Name && a.Size == f.Entry.Size && (f.Entry.SHA1 == "" || strings.EqualFold(a.SHA1, f.Entry.SHA1)) {
					found = true
					break
				}
			}
			if !found {
				missing = f.Relative
				break
			}
		}
		if missing == "" {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return fmt.Errorf("补充转存结果待核对：%s；重试只核对，不重复转存", missing)
		case <-timer.C:
		}
	}
}
