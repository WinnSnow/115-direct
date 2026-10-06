package organize

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type OrganizeRequest struct {
	IDs          []string `json:"ids"`
	Kind         string   `json:"kind"`
	TMDBID       int64    `json:"tmdb_id"`
	Target       string   `json:"target"`
	Mode         string   `json:"mode"`
	Season       *int     `json:"season,omitempty"`
	Episode      *int     `json:"episode,omitempty"`
	Offset       int      `json:"offset"`
	AllowSpecial bool     `json:"allow_special"`
	AllowMulti   bool     `json:"allow_multi"`
	Manual       bool     `json:"manual"`
	Digest       string   `json:"digest,omitempty"`
}
type PlanItem struct {
	Media    store.MediaEntry   `json:"media"`
	Link     store.MediaLink    `json:"link"`
	Previous *store.MediaLink   `json:"previous,omitempty"`
	Retire   []string           `json:"retire"`
	Skip     string             `json:"skip,omitempty"`
	Upload   *uploadArchivePlan `json:"upload_archive,omitempty"`
}
type OrganizePlan struct {
	Items        []PlanItem      `json:"items"`
	Details      *tmdb.Details   `json:"details"`
	Request      OrganizeRequest `json:"request"`
	Digest       string          `json:"digest"`
	RetirePolicy string          `json:"retire_policy"`
}

func (s *Service) Directories(ctx context.Context) DirectoryConfig {
	c := s.Defaults
	_ = s.Store.GetSetting(ctx, "directories", &c)
	c.STRMPath = first(c.STRMPath, s.Defaults.STRMPath)
	c.PendingPath = first(c.PendingPath, s.Defaults.PendingPath)
	c.GatewayURL = first(c.GatewayURL, s.Defaults.GatewayURL)
	return c
}
func (s *Service) Options(ctx context.Context) Options {
	o := DefaultOptions()
	_ = s.Store.GetSetting(ctx, "organization", &o)
	o.Normalize()
	return o
}

func (s *Service) Preview(ctx context.Context, r OrganizeRequest) (*OrganizePlan, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	r.Manual = true
	d, err := s.TMDB.Details(ctx, r.Kind, r.TMDBID)
	if err != nil {
		return nil, err
	}
	return s.buildPlan(ctx, s.Directories(ctx), r, d)
}

func (s *Service) buildPlan(ctx context.Context, c DirectoryConfig, r OrganizeRequest, d *tmdb.Details) (*OrganizePlan, error) {
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return nil, err
	}
	if len(r.IDs) == 0 || len(r.IDs) > 500 {
		return nil, fmt.Errorf("请选择 1 至 500 个文件")
	}
	if r.Episode != nil && len(r.IDs) != 1 {
		return nil, fmt.Errorf("指定集号时只能选择一个文件；批量请使用集号偏移")
	}
	if r.Target != "" && (!filepath.IsLocal(r.Target) || r.Target == ".") {
		return nil, fmt.Errorf("目标子目录无效")
	}
	o := s.Options(ctx)
	versionPolicy := o.Policy(d.Kind)
	if r.Mode != "" {
		o.Mode = r.Mode
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return nil, err
	}
	occupied := map[string]string{}
	for _, l := range links {
		if l.Deleted == "" && l.OutputPath != "" {
			occupied[l.OutputPath] = l.RemoteID
		}
	}
	ledger, err := s.Store.ListMedia(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range ledger {
		if within(c.STRMPath, m.STRMPath) {
			occupied[m.STRMPath] = m.RemoteID
		}
	}
	plan := &OrganizePlan{Items: []PlanItem{}, Details: d, Request: r, RetirePolicy: o.RetirePolicy}
	seen := map[string]bool{}
	for _, id := range r.IDs {
		if seen[id] {
			return nil, fmt.Errorf("重复文件选择")
		}
		seen[id] = true
		m, err := s.Store.GetMediaByRemoteID(ctx, id)
		if err != nil {
			return nil, err
		}
		l, err := s.Store.MediaLink(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			l = &store.MediaLink{RemoteID: id, SourcePath: m.STRMPath, IngestedAt: m.UpdatedAt}
			if within(c.STRMPath, m.STRMPath) {
				l.OutputPath = m.STRMPath
			}
		} else if err != nil {
			return nil, err
		}
		if l.Unavailable || m.Missing > 0 {
			return nil, fmt.Errorf("云端源不可用: %s", m.Name)
		}
		if l.Deleted != "" && (!r.Manual || l.Deleted != "output") {
			return nil, fmt.Errorf("文件已暂停自动整理，需明确恢复: %s", m.Name)
		}
		if l.OutputPath != "" && l.Deleted == "" {
			if _, err := os.Lstat(l.OutputPath); err != nil {
				return nil, fmt.Errorf("成品缺失或挂载离线，先处理待确认记录: %s", m.Name)
			}
		}
		source := l.SourcePath
		if source == "" || l.Mode == "move" {
			source = m.STRMPath
		}
		if _, err := os.Stat(source); os.IsNotExist(err) && l.OutputPath != "" && l.Deleted == "" {
			source = l.OutputPath
		}
		if !within(c.PendingPath, source) && !within(c.STRMPath, source) {
			return nil, fmt.Errorf("源路径超出配置根目录")
		}
		if err := validateMetadataDir(firstRoot(c, source), filepath.Dir(source)); err != nil {
			return nil, err
		}
		info, err := os.Lstat(source)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("整理源必须为普通 STRM 文件")
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return nil, err
		}
		if string(content) != s.mediaContent(c, *m) {
			return nil, fmt.Errorf("源 STRM 已被修改: %s", m.Name)
		}
		ep := Episode{}
		if d.Kind == "tv" {
			re := regexp.MustCompile(o.EpisodeRegex)
			if len(re.FindAllStringSubmatch(m.Name, -1)) > 1 && r.Episode == nil {
				return nil, fmt.Errorf("多个季集标记不明确，请人工指定单文件季集")
			}
			matches := re.FindStringSubmatch(m.Name)
			if len(matches) >= 3 {
				fmt.Sscanf(matches[1], "%d", &ep.Season)
				fmt.Sscanf(matches[2], "%d", &ep.Start)
				ep.End = ep.Start
				if len(matches) > 3 && matches[3] != "" {
					fmt.Sscanf(matches[3], "%d", &ep.End)
				}
			}
			if r.Season != nil {
				ep.Season = *r.Season
			}
			if r.Episode != nil {
				ep.Start = *r.Episode
				ep.End = *r.Episode
			}
			ep.Start += r.Offset
			ep.End += r.Offset
			if ep.Season < 0 || ep.Start < 1 || ep.End < ep.Start || ep.End > 999 || ep.Season > 99 {
				return nil, fmt.Errorf("季集不明确或偏移后无效: %s", m.Name)
			}
			if ep.Season == 0 && !r.AllowSpecial {
				return nil, fmt.Errorf("特别篇需人工确认")
			}
			if ep.End != ep.Start && !r.AllowMulti {
				return nil, fmt.Errorf("多集范围需人工确认")
			}
		}
		v := templateValues(d, m.Name, ep)
		template := o.MovieTemplate
		if d.Kind == "tv" {
			template = o.TVTemplate
		}
		rel, err := RenderTemplate(template, v)
		if err != nil {
			return nil, err
		}
		category, sub := s.mediaDirectories(ctx, d)
		if sub == "" && r.Target == "" {
			return nil, fmt.Errorf("分类不明确，请指定目标子目录")
		}
		prefix := filepath.Join(category, sub)
		if r.Target != "" {
			prefix = r.Target
		}
		if versionPolicy == "coexist" {
			label := qualityLabel(ParseQuality(m.Name))
			rel = strings.TrimSuffix(rel, ".strm") + " - " + label + " [v-" + stableSuffix(id) + "].strm"
		}
		path := filepath.Join(c.STRMPath, prefix, rel)
		if err := checkDestination(c.STRMPath, path); err != nil {
			return nil, err
		}
		newLink := *l
		newLink.OutputPath = path
		newLink.TitlePath = filepath.Dir(path)
		if d.Kind == "tv" {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) < 3 {
				return nil, fmt.Errorf("电视剧模板须包含作品目录和季目录")
			}
			newLink.TitlePath = filepath.Join(c.STRMPath, prefix, parts[0])
		}
		newLink.Mode = o.Mode
		newLink.Kind = d.Kind
		newLink.TMDBID = d.ID
		newLink.Season = ep.Season
		newLink.Episode = ep.Start
		newLink.EpisodeEnd = ep.End
		newLink.Part = v["part"]
		newLink.Manual = r.Manual
		if l.Manual && !r.Manual {
			return nil, fmt.Errorf("人工匹配文件不自动覆盖")
		}
		newLink.Deleted = ""
		newLink.Suppressed = ""
		newLink.Quality = ParseQuality(m.Name)
		if newLink.IngestedAt.IsZero() {
			newLink.IngestedAt = m.UpdatedAt
		}
		newLink.SourcePath = l.SourcePath
		if newLink.SourcePath == "" {
			newLink.SourcePath = source
		}
		newLink.VersionGroup = fmt.Sprintf("%s:%d:%d:%d-%d:%s", d.Kind, d.ID, ep.Season, ep.Start, ep.End, newLink.Part)
		item := PlanItem{Media: *m, Link: newLink, Previous: l, Retire: []string{}}
		if l.Deleted == "" && l.OutputPath == path && l.Mode == o.Mode && l.VersionGroup == newLink.VersionGroup {
			item.Skip = "已完成相同整理"
		}
		for _, old := range links {
			if old.RemoteID == id || old.Deleted != "" || old.OutputPath == "" {
				continue
			}
			if old.TitlePath == newLink.TitlePath && old.TMDBID != newLink.TMDBID {
				return nil, fmt.Errorf("作品目录已有其他TMDB关联，禁止覆盖共享元数据")
			}
			if d.Kind == "tv" && old.Kind == d.Kind && old.TMDBID == d.ID && old.Season == ep.Season && old.Episode <= ep.End && old.EpisodeEnd >= ep.Start && old.VersionGroup != newLink.VersionGroup {
				return nil, fmt.Errorf("季集范围与现有版本重叠但不一致，需人工处理")
			}
			if old.VersionGroup != newLink.VersionGroup {
				continue
			}
			oldMedia, err := s.Store.GetMediaByRemoteID(ctx, old.RemoteID)
			if err != nil {
				return nil, err
			}
			if m.SHA1 != "" && strings.EqualFold(m.SHA1, oldMedia.SHA1) {
				item.Skip = "SHA1 与现有版本相同"
				continue
			}
			if versionPolicy == "coexist" {
				continue
			}
			prefer := preferVersion(versionPolicy, newLink, old)
			if !prefer {
				item.Skip = "现有版本优先"
			} else {
				item.Retire = append(item.Retire, old.RemoteID)
			}
		}
		if owner := occupied[path]; owner != "" && owner != id {
			path = filepath.Join(filepath.Dir(path), versionedName(filepath.Base(path), stableSuffix(id)))
			item.Link.OutputPath = path
		}
		if owner := occupied[path]; owner != "" && owner != id {
			return nil, fmt.Errorf("目标路径冲突")
		}
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return nil, fmt.Errorf("目标不是 STRM 文件")
			}
			if occupied[path] != id {
				return nil, fmt.Errorf("目标存在未关联的文件")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if o.Mode == "hardlink" {
			if err := probeHardlink(source, c.STRMPath); err != nil {
				return nil, err
			}
		}
		if l.Mode == "upload" && l.OutputPath == "" {
			upload, err := s.previewUploadArchive(ctx, c, *m, *l, category, sub)
			if err != nil {
				return nil, err
			}
			item.Upload = upload
			item.Link.SourcePath = filepath.Join(c.PendingPath, strings.TrimSuffix(upload.Relative, filepath.Ext(upload.Relative))+".strm")
		}
		if o.Mode == "move" {
			for _, old := range links {
				if old.RemoteID != id && old.Deleted == "" && old.Mode == "symlink" && old.SourcePath == source {
					return nil, fmt.Errorf("源文件仍有成品软链接引用")
				}
			}
		}
		item.Media.STRMPath = path
		occupied[path] = id
		plan.Items = append(plan.Items, item)
	}
	// Resolve equal version groups before publishing any member of the batch.
	for i := range plan.Items {
		for j := i + 1; j < len(plan.Items); j++ {
			a, b := &plan.Items[i], &plan.Items[j]
			if a.Skip != "" || b.Skip != "" {
				continue
			}
			if a.Link.Kind == "tv" && a.Link.Season == b.Link.Season && a.Link.Episode <= b.Link.EpisodeEnd && a.Link.EpisodeEnd >= b.Link.Episode && a.Link.VersionGroup != b.Link.VersionGroup {
				return nil, fmt.Errorf("所选文件季集范围重叠但不一致，需人工分开处理")
			}
			if a.Link.VersionGroup == b.Link.VersionGroup {
				if a.Media.SHA1 != "" && strings.EqualFold(a.Media.SHA1, b.Media.SHA1) {
					b.Skip = "SHA1 与同批版本相同"
				} else if versionPolicy != "coexist" {
					if preferVersion(versionPolicy, b.Link, a.Link) {
						a.Skip = "同批其他版本优先"
					} else {
						b.Skip = "同批其他版本优先"
					}
				}
			}
		}
	}
	plan.Request.Digest = ""
	raw, _ := json.Marshal(plan)
	plan.Digest = store.ContentHash(raw)
	return plan, nil
}
func preferVersion(policy string, incoming, existing store.MediaLink) bool {
	switch policy {
	case "overwrite":
		return true
	case "newest":
		return incoming.IngestedAt.After(existing.IngestedAt)
	case "quality":
		return incoming.Quality.Resolution > existing.Quality.Resolution ||
			(incoming.Quality.Resolution == existing.Quality.Resolution && incoming.IngestedAt.After(existing.IngestedAt))
	default:
		return false
	}
}
func stableSuffix(id string) string { h := sha256.Sum256([]byte(id)); return fmt.Sprintf("%x", h[:6]) }
func qualityLabel(q store.Quality) string {
	if q.Resolution == 0 {
		return "UNKNOWN"
	}
	return fmt.Sprintf("%dP", q.Resolution)
}
func firstRoot(c DirectoryConfig, path string) string {
	if within(c.PendingPath, path) {
		return c.PendingPath
	}
	return c.STRMPath
}
func checkDestination(root, path string) error {
	if !within(root, path) || path == root {
		return fmt.Errorf("路径超出根目录")
	}
	p := filepath.Dir(path)
	for {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			p = filepath.Dir(p)
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("目标路径含软链接或非目录项")
		}
		return validateMetadataDir(root, p)
	}
}
func probeHardlink(source, root string) error {
	f, err := os.CreateTemp(root, ".hardlink-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	if err := os.Link(source, name); err != nil {
		return fmt.Errorf("硬链接要求同一文件系统: %w", err)
	}
	return os.Remove(name)
}

func publishMode(path, source, content, mode string) error {
	if mode == "copy" || mode == "move" {
		return writeAtomicReplacement(path, []byte(content))
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".placement-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	if mode == "hardlink" {
		err = os.Link(source, name)
	} else if mode == "symlink" {
		var absolute string
		absolute, err = filepath.Abs(source)
		if err == nil {
			err = os.Symlink(absolute, name)
		}
	} else {
		return fmt.Errorf("整理方式无效")
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (s *Service) mediaContent(c DirectoryConfig, m store.MediaEntry) string {
	return fmt.Sprintf("%s/direct/%s?sig=%s&jellyfin_sig=%s\n", strings.TrimRight(c.GatewayURL, "/"), m.ID, SignMedia(s.Secret, m.ID), SignJellyfinRequest(s.Secret, m.ID))
}

func (s *Service) SubmitPlan(ctx context.Context, r OrganizeRequest) (*store.TransferJob, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	r.Manual = true
	d, err := s.TMDB.Details(ctx, r.Kind, r.TMDBID)
	if err != nil {
		return nil, err
	}
	p, err := s.buildPlan(ctx, s.Directories(ctx), r, d)
	if err != nil {
		return nil, err
	}
	if r.Digest == "" || r.Digest != p.Digest {
		return nil, fmt.Errorf("预览已变化，请重新预览")
	}
	id, err := randomID(12)
	if err != nil {
		return nil, err
	}
	j := &store.TransferJob{ID: id, Source: "manual", Title: d.Title, Status: "queued", TMDBKind: d.Kind, TMDBID: d.ID}
	j.Actual, _ = json.Marshal(p)
	if err := s.Store.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	s.Enqueue(id)
	return j, nil
}
func (s *Service) processManual(ctx context.Context, c DirectoryConfig, j *store.TransferJob, fail func(error) error) error {
	var p OrganizePlan
	if err := json.Unmarshal(j.Actual, &p); err != nil {
		return fail(err)
	}
	j.Status = "organizing"
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return fail(err)
	}
	items, err := s.executePlan(ctx, c, j.ID, &p)
	if err != nil {
		return fail(err)
	}
	if err := s.writeMetadata(ctx, c, p.Details, items); err != nil {
		return fail(err)
	}
	if err := s.refreshLibrary(ctx); err != nil {
		return fail(err)
	}
	j.Status = "completed"
	j.Error = ""
	return s.Store.UpdateJob(ctx, j)
}

func (s *Service) finishModern(ctx context.Context, c DirectoryConfig, j *store.TransferJob, r OrganizeRequest, d *tmdb.Details, fail func(error) error) error {
	var p OrganizePlan
	if err := s.Store.GetSetting(ctx, "organize_plan:"+j.ID, &p); errors.Is(err, sql.ErrNoRows) {
		plan, err := s.buildPlan(ctx, c, r, d)
		if err != nil {
			j.Status = "waiting_match"
			j.Error = err.Error()
			return s.Store.UpdateJob(ctx, j)
		}
		p = *plan
		if err := s.Store.PutSetting(ctx, "organize_plan:"+j.ID, p); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	}
	items, err := s.executePlan(ctx, c, j.ID, &p)
	if err != nil {
		return fail(err)
	}
	if len(items) > 0 {
		if err := s.writeMetadata(ctx, c, d, items); err != nil {
			return fail(err)
		}
	}
	if err := s.refreshLibrary(ctx); err != nil {
		return fail(err)
	}
	j.Status = "completed"
	j.Error = ""
	j.CleanupAt = nil
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		return err
	}
	if j.Source == "wecom" || j.Source == "web" {
		s.notify(j, "整理完成", fmt.Sprintf("%s，已核验并整理 %d 个本地 STRM；接收原件保留", d.Title, len(items)))
	}
	return nil
}

type fileStep struct {
	Config       DirectoryConfig `json:"config"`
	Item         PlanItem        `json:"item"`
	Content      string          `json:"content"`
	RetirePolicy string          `json:"retire_policy"`
}

func (s *Service) executePlan(ctx context.Context, c DirectoryConfig, jobID string, p *OrganizePlan) ([]store.MediaEntry, error) {
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return nil, err
	}
	items := []store.MediaEntry{}
	for index, item := range p.Items {
		if item.Upload != nil && item.Skip == "" {
			if err := s.archiveUploadItem(ctx, c, jobID, &item); err != nil {
				return nil, err
			}
			p.Items[index] = item
			for k, id := range p.Request.IDs {
				if id == item.Upload.OriginalID {
					p.Request.IDs[k] = item.Media.RemoteID
				}
			}
			if err := s.persistArchivedPlan(ctx, jobID, p); err != nil {
				return nil, err
			}
		}
		if item.Skip != "" {
			s.Store.Audit(ctx, "organize", item.Media.Name+": "+item.Skip)
			if item.Skip == "已完成相同整理" {
				items = append(items, item.Media)
			} else if item.Previous != nil && item.Previous.OutputPath == "" {
				l := item.Link
				if item.Upload != nil {
					l.SourcePath = item.Previous.SourcePath
				}
				l.OutputPath = ""
				l.Suppressed = item.Skip
				if err := s.Store.PutLink(ctx, l); err != nil {
					return nil, err
				}
			}
			continue
		}
		id := "organize:" + jobID + ":" + item.Media.RemoteID
		e, err := s.Store.Execution(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			raw, _ := json.Marshal(fileStep{c, item, s.mediaContent(c, item.Media), p.RetirePolicy})
			e = &store.Execution{ID: id, Kind: "organize", Status: "planned", Body: raw}
			if err := s.Store.PutExecution(ctx, *e); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if err := s.resumeFile(ctx, e); err != nil {
			return nil, err
		}
		items = append(items, item.Media)
	}
	return items, nil
}

func (s *Service) resumeFile(ctx context.Context, e *store.Execution) (result error) {
	if e.Status == "completed" {
		return nil
	}
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var step fileStep
	if err := json.Unmarshal(e.Body, &step); err != nil {
		return err
	}
	c := step.Config
	i := step.Item
	path := i.Link.OutputPath
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return err
	}
	set := func(status string) error { e.Status = status; e.Error = ""; return s.Store.PutExecution(ctx, *e) }
	if e.Status == "planned" {
		current, err := s.Store.MediaLink(ctx, i.Media.RemoteID)
		if err == nil && i.Previous != nil {
			if current.Deleted != i.Previous.Deleted || current.OutputPath != i.Previous.OutputPath {
				return fmt.Errorf("关联已变化，执行暂停；重新预览")
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := checkDestination(c.STRMPath, path); err != nil {
			return err
		}
		if err := EnsureLocalDirectory(c.STRMPath, filepath.Dir(path)); err != nil {
			return err
		}
		source := i.Link.SourcePath
		if _, err := os.Stat(source); os.IsNotExist(err) && i.Previous != nil && i.Previous.OutputPath != "" && i.Previous.Deleted == "" {
			source = i.Previous.OutputPath
		}
		if i.Link.Mode == "symlink" || i.Link.Mode == "hardlink" {
			if !within(c.PendingPath, i.Link.SourcePath) {
				return fmt.Errorf("链接源必须位于待整理目录")
			}
			if _, err := os.Stat(i.Link.SourcePath); os.IsNotExist(err) {
				if err := EnsureLocalDirectory(c.PendingPath, filepath.Dir(i.Link.SourcePath)); err != nil {
					return err
				}
				if err := writeAtomic(i.Link.SourcePath, []byte(step.Content)); err != nil {
					return err
				}
				source = i.Link.SourcePath
			} else if err != nil {
				return err
			}
		}
		if i.Previous != nil && i.Previous.Mode == "move" {
			source = i.Previous.OutputPath
			if i.Link.Mode != "move" {
				if !within(c.PendingPath, i.Link.SourcePath) {
					return fmt.Errorf("待整理路径越界")
				}
				if err := EnsureLocalDirectory(c.PendingPath, filepath.Dir(i.Link.SourcePath)); err != nil {
					return err
				}
				if _, err := os.Stat(i.Link.SourcePath); os.IsNotExist(err) {
					if err := writeAtomic(i.Link.SourcePath, []byte(step.Content)); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
				source = i.Link.SourcePath
			}
		}
		if !within(c.PendingPath, source) && !within(c.STRMPath, source) {
			return fmt.Errorf("源路径越界")
		}
		if err := validateMetadataDir(firstRoot(c, source), filepath.Dir(source)); err != nil {
			return err
		}
		if old, err := os.ReadFile(path); err == nil && string(old) == step.Content {
			// A process can stop after publishing the file but before persisting its step.
			if i.Previous != nil && i.Previous.OutputPath == path && i.Previous.Mode != i.Link.Mode {
				if err := publishMode(path, source, step.Content, i.Link.Mode); err != nil {
					return err
				}
			}
		} else {
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				return fmt.Errorf("目标内容冲突，已保留原文件")
			}
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			if string(data) != step.Content {
				return fmt.Errorf("源 STRM 内容已变化")
			}
			if i.Link.Mode == "copy" || i.Link.Mode == "move" {
				if err := writeAtomic(path, data); err != nil {
					return err
				}
			} else if i.Link.Mode == "hardlink" {
				if err := os.Link(source, path); err != nil {
					return err
				}
			} else if i.Link.Mode == "symlink" {
				absolute, err := filepath.Abs(source)
				if err != nil {
					return err
				}
				if err := os.Symlink(absolute, path); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("整理方式无效")
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != step.Content {
			return fmt.Errorf("成品写入验证失败")
		}
		if err := set("published"); err != nil {
			return err
		}
	}
	if e.Status == "published" {
		if err := s.Store.PutLink(ctx, i.Link); err != nil {
			return err
		}
		rel, err := filepath.Rel(c.STRMPath, path)
		if err != nil {
			return err
		}
		if err := s.Store.SetLocalOrganization(ctx, i.Media.RemoteID, rel); err != nil {
			return err
		}
		if err := s.Store.PutMedia(ctx, i.Media); err != nil {
			return err
		}
		if err := set("mapped"); err != nil {
			return err
		}
	}
	if e.Status == "mapped" {
		if i.Link.Mode == "move" && i.Link.SourcePath != path {
			if err := s.removeOwnedSTRM(ctx, c, i.Link.SourcePath, step.Content, i.Media.RemoteID); err != nil {
				return err
			}
		}
		if i.Previous != nil && i.Previous.OutputPath != "" && i.Previous.OutputPath != path {
			if err := s.removeOwnedSTRM(ctx, c, i.Previous.OutputPath, step.Content, i.Media.RemoteID); err != nil {
				return err
			}
			if err := s.releaseAssets(ctx, c, i.Media.RemoteID, path); err != nil {
				return err
			}
			removeEmptyLocalParents(filepath.Dir(i.Previous.OutputPath), c.STRMPath)
		}
		for _, id := range i.Retire {
			if err := s.deleteMediaLocked(ctx, c, id, step.RetirePolicy, e.ID+":retire:"+id); err != nil {
				return err
			}
		}
		if err := set("completed"); err != nil {
			return err
		}
		s.Store.Audit(ctx, "organize", fmt.Sprintf("任务 %s 文件 %s: %s -> %s；方式 %s，版本组 %s", e.ID, i.Media.ID, i.Link.SourcePath, path, i.Link.Mode, i.Link.VersionGroup))
	}
	return nil
}

func (s *Service) Recover(ctx context.Context) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	steps, err := s.Store.RecoverableExecutions(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < len(steps); i++ {
		e := steps[i]
		if e.Status == "completed" || e.Error != "" {
			continue
		}
		var err error
		switch e.Kind {
		case "organize":
			err = s.resumeFile(ctx, &e)
		case "upload_archive":
			err = s.resumeUploadArchive(ctx, &e)
		case "delete":
			err = s.resumeDelete(ctx, &e)
		case "file":
			err = s.resumeOperation(ctx, &e)
		case "asset":
			err = s.resumeAsset(ctx, &e)
		case "image":
			err = s.resumeImage(ctx, &e)
		}
		if err != nil {
			s.Store.Audit(ctx, "file", fmt.Sprintf("恢复 %s 待重试: %v", e.ID, err))
		}
	}
	return nil
}

func (s *Service) registerSources(ctx context.Context, c DirectoryConfig, files []sourceFile, inboxID string) ([]string, error) {
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, f := range files {
		if classify(f.Entry.Name) != Video {
			continue
		}
		id := f.Entry.ID
		m, err := s.Store.GetMediaByRemoteID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			mid, err := randomID(16)
			if err != nil {
				return nil, err
			}
			m = &store.MediaEntry{ID: mid, RemoteID: id}
		} else if err != nil {
			return nil, err
		}
		m.Name = f.Entry.Name
		m.PickCode = f.Entry.PickCode
		m.SHA1 = f.Entry.SHA1
		m.RemotePath = f.Relative
		l, err := s.Store.MediaLink(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			l = &store.MediaLink{RemoteID: id, InboxID: inboxID, IngestedAt: time.Now().UTC(), SourcePath: filepath.Join(c.PendingPath, strings.TrimSuffix(f.Relative, filepath.Ext(f.Relative))+".strm")}
		} else if err != nil {
			return nil, err
		}
		if l.Deleted != "" {
			continue
		}
		if !within(c.PendingPath, l.SourcePath) {
			return nil, fmt.Errorf("待整理路径越界")
		}
		if err := EnsureLocalDirectory(c.PendingPath, filepath.Dir(l.SourcePath)); err != nil {
			return nil, err
		}
		content := s.mediaContent(c, *m)
		if existing, err := os.ReadFile(l.SourcePath); os.IsNotExist(err) {
			if err := writeAtomic(l.SourcePath, []byte(content)); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else if string(existing) != content {
			return nil, fmt.Errorf("待整理文件冲突")
		}
		m.STRMPath = l.OutputPath
		if m.STRMPath == "" {
			m.STRMPath = l.SourcePath
		}
		if err := s.Store.PutLink(ctx, *l); err != nil {
			return nil, err
		}
		if err := s.Store.PutMedia(ctx, *m); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Prevent indirect cleanup from breaking another active symlink or shared mapping.
func (s *Service) removeOwnedSTRM(ctx context.Context, c DirectoryConfig, path, content, except string) error {
	if !within(c.PendingPath, path) && !within(c.STRMPath, path) {
		return fmt.Errorf("清理路径越界")
	}
	if err := validateMetadataDir(firstRoot(c, path), filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.RemoteID != except && l.Deleted == "" && (l.OutputPath == path || (l.Mode == "symlink" && l.SourcePath == path)) {
			s.Store.Audit(ctx, "file", "保留共享对象: "+path+" 引用 "+l.RemoteID)
			return nil
		}
		if l.RemoteID != except && l.Deleted == "" {
			for _, copy := range l.Copies {
				if copy == path {
					s.Store.Audit(ctx, "file", "保留共享副本: "+path)
					return nil
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(data) != content {
		return fmt.Errorf("文件已被修改，保留: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}
