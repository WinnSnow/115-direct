package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type FileRequest struct {
	Scope  string   `json:"scope"`
	Action string   `json:"action"`
	Paths  []string `json:"paths"`
	Target string   `json:"target"`
	Digest string   `json:"digest,omitempty"`
}
type FileChange struct {
	Source    string            `json:"source"`
	Target    string            `json:"target"`
	RemoteID  string            `json:"remote_id,omitempty"`
	Directory bool              `json:"directory"`
	Before    []string          `json:"before,omitempty"`
	Name      string            `json:"name,omitempty"`
	Manifest  map[string]string `json:"manifest,omitempty"`
	Related   []FileChange      `json:"related,omitempty"`
}
type FilePlan struct {
	Request  FileRequest       `json:"request"`
	Config   DirectoryConfig   `json:"config"`
	Changes  []FileChange      `json:"changes"`
	Affected []store.MediaLink `json:"affected"`
	Assets   []store.Asset     `json:"assets"`
	Digest   string            `json:"digest"`
	Policy   string            `json:"policy"`
}

func (s *Service) PreviewFiles(ctx context.Context, r FileRequest) (*FilePlan, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.previewFiles(ctx, r)
}
func (s *Service) previewFiles(ctx context.Context, r FileRequest) (*FilePlan, error) {
	c := s.Directories(ctx)
	if r.Scope != "115" || r.Action != "mkdir" {
		if err := CheckRoots(ctx, s.Store, c); err != nil {
			return nil, err
		}
	}
	if r.Action != "mkdir" && r.Action != "rename" && r.Action != "move" && r.Action != "copy" && r.Action != "delete" {
		return nil, fmt.Errorf("文件操作无效")
	}
	if len(r.Paths) == 0 || len(r.Paths) > 100 {
		return nil, fmt.Errorf("请选择 1 至 100 项")
	}
	if r.Action == "rename" && len(r.Paths) != 1 {
		return nil, fmt.Errorf("重命名每次只能选择一项")
	}
	p := &FilePlan{Request: r, Config: c, Changes: []FileChange{}, Affected: []store.MediaLink{}, Assets: []store.Asset{}, Policy: s.Options(ctx).DeletePolicy}
	if r.Scope == "115" {
		if err := s.previewCloud(ctx, p); err != nil {
			return nil, err
		}
	} else {
		root := c.STRMPath
		if r.Scope == "pending" {
			root = c.PendingPath
		} else if r.Scope != "output" {
			return nil, fmt.Errorf("目录视图无效")
		}
		links, err := s.Store.ListLinks(ctx)
		if err != nil {
			return nil, err
		}
		assets, err := s.Store.Assets(ctx)
		if err != nil {
			return nil, err
		}
		for _, relative := range r.Paths {
			if !filepath.IsLocal(relative) || filepath.Clean(relative) == "." {
				return nil, fmt.Errorf("根目录或越界路径禁止操作")
			}
			source := filepath.Join(root, relative)
			if err := validateMetadataDir(root, filepath.Dir(source)); err != nil {
				return nil, err
			}
			change := FileChange{Source: source}
			if r.Action == "mkdir" {
				change.Target = source
				if err := checkDestination(root, source); err != nil {
					return nil, err
				}
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					return nil, fmt.Errorf("目录已存在或不可访问")
				}
			} else {
				info, err := os.Lstat(source)
				if err != nil {
					return nil, err
				}
				change.Directory = info.IsDir()
				manifest, err := treeManifest(source)
				if err != nil {
					return nil, err
				}
				change.Manifest = manifest
				if r.Action == "copy" {
					for _, hash := range manifest {
						if strings.HasPrefix(hash, "symlink:") {
							return nil, fmt.Errorf("复制含软链接的目录请先将关联整理方式改为复制")
						}
					}
				}
				if info.Mode()&os.ModeSymlink != 0 {
					known := false
					for _, l := range links {
						if l.OutputPath == source && l.Mode == "symlink" {
							known = true
						}
					}
					if !known {
						return nil, fmt.Errorf("未关联的软链接禁止操作")
					}
				}
				if r.Action != "delete" {
					if r.Target != "" && (!filepath.IsLocal(r.Target) || r.Target == ".") {
						return nil, fmt.Errorf("目标路径无效")
					}
					change.Target = filepath.Join(root, r.Target, filepath.Base(source))
					if r.Action == "rename" {
						change.Target = filepath.Join(root, r.Target)
					}
					if change.Target == source || within(source, change.Target) {
						return nil, fmt.Errorf("源目标相同或目标位于源目录内")
					}
					if err := checkDestination(root, change.Target); err != nil {
						return nil, err
					}
					if _, err := os.Lstat(change.Target); !os.IsNotExist(err) {
						return nil, fmt.Errorf("目标已存在或不可访问")
					}
				}
				managed := false
				for _, l := range links {
					affected := within(source, l.SourcePath) || within(source, l.OutputPath)
					for _, copy := range l.Copies {
						if within(source, copy) {
							affected = true
						}
					}
					if !affected || l.Deleted != "" {
						continue
					}
					managed = true
					if (r.Action == "move" || r.Action == "rename" || r.Action == "delete") && l.Mode == "symlink" && within(source, l.SourcePath) && !within(source, l.OutputPath) {
						return nil, fmt.Errorf("操作会影响成品软链接 %s，请先将引用改为复制", l.OutputPath)
					}
					if (r.Action == "move" || r.Action == "rename") && within(source, l.OutputPath) && l.TitlePath != "" && !within(source, l.TitlePath) && !within(l.TitlePath, change.Target) {
						return nil, fmt.Errorf("操作将拆分作品与元数据目录，请使用整理/纠错重新指定目标")
					}
					if r.Scope == "pending" && r.Action == "delete" {
						return nil, fmt.Errorf("关联待整理文件请从成品删除预览按联动策略处理")
					}
					if r.Action == "copy" && r.Scope == "pending" {
						return nil, fmt.Errorf("待整理文件复制请使用整理预览")
					}
					p.Affected = append(p.Affected, l)
				}
				for _, a := range assets {
					if within(source, a.Path) {
						p.Assets = append(p.Assets, a)
						managed = true
					}
				}
				if !change.Directory && strings.EqualFold(filepath.Ext(source), ".strm") && r.Action != "delete" {
					if !strings.EqualFold(filepath.Ext(change.Target), ".strm") {
						return nil, fmt.Errorf("关联媒体必须保留 .strm 扩展名")
					}
					nfo := strings.TrimSuffix(source, filepath.Ext(source)) + ".nfo"
					for _, a := range assets {
						if a.Path != nfo {
							continue
						}
						target := strings.TrimSuffix(change.Target, filepath.Ext(change.Target)) + ".nfo"
						if _, err := os.Lstat(target); !os.IsNotExist(err) {
							return nil, fmt.Errorf("目标 NFO 已存在或不可访问")
						}
						manifest, err := treeManifest(nfo)
						if err != nil {
							return nil, err
						}
						change.Related = append(change.Related, FileChange{Source: nfo, Target: target, Manifest: manifest})
						p.Assets = append(p.Assets, a)
					}
				}
				if r.Action == "delete" && !managed {
					return nil, fmt.Errorf("未关联文件禁止联动删除")
				}
				if r.Action == "delete" && change.Directory {
					for rel := range change.Manifest {
						if rel == "." {
							continue
						}
						full := filepath.Join(source, rel)
						info, err := os.Lstat(full)
						if err != nil {
							return nil, err
						}
						if info.IsDir() {
							continue
						}
						known := false
						for _, l := range links {
							if l.OutputPath == full {
								known = true
							}
						}
						for _, a := range assets {
							if a.Path == full {
								known = true
							}
						}
						if !known {
							return nil, fmt.Errorf("目录含未关联或用户文件，禁止批量删除: %s", rel)
						}
					}
				}
			}
			for _, existing := range p.Changes {
				if within(existing.Source, source) || within(source, existing.Source) {
					return nil, fmt.Errorf("批量范围重复或相互包含")
				}
			}
			p.Changes = append(p.Changes, change)
		}
	}
	p.Request.Digest = ""
	raw, _ := json.Marshal(p)
	p.Digest = store.ContentHash(raw)
	return p, nil
}

func (s *Service) cloudTree(ctx context.Context, cid string, entries map[string]pan115.Entry) error {
	children, err := s.Pan.List(ctx, cid)
	if err != nil {
		return err
	}
	for _, e := range children {
		if _, ok := entries[e.ID]; ok {
			return fmt.Errorf("115 目录循环")
		}
		entries[e.ID] = e
		if e.Directory {
			if err := s.cloudTree(ctx, e.ID, entries); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) previewCloud(ctx context.Context, p *FilePlan) error {
	// 新建空目录不修改现有媒体关联，也不依赖本地挂载或分类库。
	// 只列举用户选定的父目录，避免为新建目录递归扫描整个媒体库。
	if p.Request.Action == "mkdir" {
		return s.previewCloudMkdir(ctx, p)
	}
	c := p.Config
	if c.LibraryCID == "" || c.LibraryCID == c.InboxCID {
		return fmt.Errorf("115 分类根目录无效")
	}
	entries := map[string]pan115.Entry{}
	if err := s.cloudTree(ctx, c.LibraryCID, entries); err != nil {
		return err
	}
	if _, ok := entries[c.InboxCID]; ok {
		return fmt.Errorf("分类根目录包含接收原件，禁止文件操作")
	}
	links, err := s.Store.ListLinks(ctx)
	if err != nil {
		return err
	}
	for _, id := range p.Request.Paths {
		e, ok := entries[id]
		if !ok {
			return fmt.Errorf("文件不在115分类根目录内")
		}
		change := FileChange{Source: id, RemoteID: id, Directory: e.Directory, Target: p.Request.Target}
		change.Name = e.Name
		if p.Request.Action == "rename" && !validCloudName(p.Request.Target) {
			return fmt.Errorf("名称无效")
		}
		if p.Request.Action == "move" || p.Request.Action == "copy" {
			target, ok := entries[p.Request.Target]
			if p.Request.Target != c.LibraryCID && (!ok || !target.Directory) {
				return fmt.Errorf("目标不在分类根目录内")
			}
			before, err := s.Pan.List(ctx, p.Request.Target)
			if err != nil {
				return err
			}
			for _, child := range before {
				change.Before = append(change.Before, child.ID)
				if child.Name == e.Name {
					return fmt.Errorf("115目标存在同名项")
				}
			}
			parent := p.Request.Target
			for parent != c.LibraryCID && parent != "" {
				if parent == id {
					return fmt.Errorf("禁止移动到自身或子目录")
				}
				parent = entries[parent].ParentID
			}
		}
		for _, l := range links {
			parent := l.RemoteID
			for parent != "" && parent != c.LibraryCID {
				if parent == id {
					p.Affected = append(p.Affected, l)
					break
				}
				parent = entries[parent].ParentID
			}
		}
		if p.Request.Action == "delete" {
			if e.Directory {
				return fmt.Errorf("云端目录删除需逐个关联文件预览，保留接收原件")
			}
			if len(p.Affected) == 0 {
				return fmt.Errorf("缺少本地关联，先同步再删除")
			}
			if p.Policy != "chain" {
				return fmt.Errorf("云端关联删除需选择全链路策略")
			}
		}
		p.Changes = append(p.Changes, change)
	}
	return nil
}

func (s *Service) previewCloudMkdir(ctx context.Context, p *FilePlan) error {
	if !validCloudName(p.Request.Target) {
		return fmt.Errorf("目录名称无效")
	}
	for _, parent := range p.Request.Paths {
		if strings.TrimSpace(parent) == "" {
			return fmt.Errorf("请选择115父目录")
		}
		children, err := s.Pan.List(ctx, parent)
		if err != nil {
			return err
		}
		change := FileChange{Source: parent, Target: p.Request.Target, Directory: true}
		for _, child := range children {
			change.Before = append(change.Before, child.ID)
			if child.Name == change.Target {
				return fmt.Errorf("115同名目录已存在")
			}
		}
		p.Changes = append(p.Changes, change)
	}
	return nil
}
func validCloudName(name string) bool {
	return strings.TrimSpace(name) != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n") && len(name) <= 240
}

func (s *Service) ExecuteFiles(ctx context.Context, r FileRequest) ([]store.Execution, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	p, err := s.previewFiles(ctx, r)
	if err != nil {
		return nil, err
	}
	if r.Digest == "" || r.Digest != p.Digest {
		return nil, fmt.Errorf("操作范围已变化，请重新预览")
	}
	items := []store.Execution{}
	// Persist every batch item before the first mutation, including independently retryable failures.
	for _, change := range p.Changes {
		id, err := randomID(12)
		if err != nil {
			return nil, err
		}
		single := *p
		single.Changes = []FileChange{change}
		raw, _ := json.Marshal(single)
		e := store.Execution{ID: "file:" + id, Kind: "file", Status: "planned", Body: raw}
		if err := s.Store.PutExecution(ctx, e); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	for i := range items {
		e := &items[i]
		_ = s.resumeOperation(ctx, e)
	}
	return items, nil
}
func replacePrefix(path, from, to string) string {
	if !within(from, path) {
		return path
	}
	rel, _ := filepath.Rel(from, path)
	return filepath.Join(to, rel)
}
func (s *Service) resumeOperation(ctx context.Context, e *store.Execution) (result error) {
	if e.Status == "completed" {
		return nil
	}
	defer func() {
		if result != nil {
			e.Error = result.Error()
			_ = s.Store.PutExecution(context.Background(), *e)
		}
	}()
	var p FilePlan
	if err := json.Unmarshal(e.Body, &p); err != nil {
		return err
	}
	if len(p.Changes) != 1 {
		return fmt.Errorf("文件步骤无效")
	}
	ch := p.Changes[0]
	r := p.Request
	if r.Scope != "115" || r.Action != "mkdir" {
		if err := CheckRoots(ctx, s.Store, p.Config); err != nil {
			return err
		}
	}
	set := func(status string) error { e.Status = status; e.Error = ""; return s.Store.PutExecution(ctx, *e) }
	if r.Action == "delete" {
		if r.Scope != "115" {
			root := p.Config.STRMPath
			if !within(root, ch.Source) {
				return fmt.Errorf("删除路径越界")
			}
			if err := validateMetadataDir(root, filepath.Dir(ch.Source)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		for _, l := range p.Affected {
			if within(ch.Source, l.OutputPath) || r.Scope == "115" {
				if err := s.deleteMediaLocked(ctx, p.Config, l.RemoteID, p.Policy, e.ID+":delete:"+l.RemoteID); err != nil {
					return err
				}
			}
		}
		if ch.Directory && r.Scope != "115" {
			removeEmptyTree(ch.Source)
		}
		return set("completed")
	}
	if r.Scope == "115" {
		if e.Status == "remote_pending" {
			if err := s.verifyCloudOperation(ctx, p); err != nil {
				return err
			}
			if err := set("remote_applied"); err != nil {
				return err
			}
		}
		if e.Status == "planned" {
			if err := set("remote_pending"); err != nil {
				return err
			}
			var err error
			switch r.Action {
			case "mkdir":
				_, err = s.Pan.Mkdir(ctx, ch.Source, ch.Target)
			case "rename":
				err = s.Pan.Rename(ctx, ch.Source, ch.Target)
			case "move":
				err = s.Pan.Move(ctx, ch.Target, ch.Source)
			case "copy":
				err = s.Pan.Copy(ctx, ch.Target, ch.Source)
			}
			if err != nil {
				return err
			}
			if err := s.verifyCloudOperation(ctx, p); err != nil {
				return err
			}
			if err := set("remote_applied"); err != nil {
				return err
			}
		}
		if e.Status == "remote_applied" && r.Action != "mkdir" {
			entries := map[string]pan115.Entry{}
			if err := s.cloudTree(ctx, p.Config.LibraryCID, entries); err != nil {
				return err
			}
			for _, link := range p.Affected {
				m, err := s.Store.GetMediaByRemoteID(ctx, link.RemoteID)
				if err != nil {
					return err
				}
				if _, ok := entries[m.RemoteID]; !ok {
					continue
				}
				parts := []string{}
				id := m.RemoteID
				for id != "" && id != p.Config.LibraryCID {
					node, ok := entries[id]
					if !ok {
						return fmt.Errorf("115关联路径不完整")
					}
					parts = append([]string{node.Name}, parts...)
					id = node.ParentID
				}
				m.RemotePath = filepath.Join(parts...)
				m.Name = entries[m.RemoteID].Name
				if err := s.Store.PutMedia(ctx, *m); err != nil {
					return err
				}
			}
		}
		s.Store.Audit(ctx, "file", fmt.Sprintf("115 %s %s -> %s；关联 %d 项", r.Action, ch.Source, ch.Target, len(p.Affected)))
		return set("completed")
	}
	root := p.Config.STRMPath
	if r.Scope == "pending" {
		root = p.Config.PendingPath
	}
	if !within(root, ch.Source) || !within(root, ch.Target) {
		return fmt.Errorf("文件步骤越界")
	}
	if e.Status == "planned" {
		if err := checkDestination(root, ch.Target); err != nil {
			return err
		}
		if r.Action == "mkdir" {
			if err := EnsureLocalDirectory(root, ch.Target); err != nil {
				return err
			}
		} else {
			if err := EnsureLocalDirectory(root, filepath.Dir(ch.Target)); err != nil {
				return err
			}
			if _, err := os.Lstat(ch.Target); os.IsNotExist(err) {
				if err := verifyManifest(ch.Source, ch.Manifest); err != nil {
					return err
				}
				if err := validateMetadataDir(root, filepath.Dir(ch.Source)); err != nil {
					return err
				}
				if r.Action == "copy" {
					if err := copyTree(ch.Source, ch.Target); err != nil {
						return err
					}
				} else {
					if err := os.Rename(ch.Source, ch.Target); err != nil {
						return err
					}
				}
			} else if err != nil {
				return err
			} else {
				if r.Action == "copy" {
					if err := verifyManifest(ch.Source, ch.Manifest); err != nil {
						return err
					}
					if err := copyTree(ch.Source, ch.Target); err != nil {
						return err
					}
				}
				if _, err := os.Lstat(ch.Source); err == nil && r.Action != "copy" {
					return fmt.Errorf("源目标同时存在，需核对")
				}
			}
			if err := verifyManifest(ch.Target, ch.Manifest); err != nil {
				return err
			}
			for _, related := range ch.Related {
				if err := checkDestination(root, related.Target); err != nil {
					return err
				}
				if _, err := os.Lstat(related.Target); os.IsNotExist(err) {
					if err := verifyManifest(related.Source, related.Manifest); err != nil {
						return err
					}
					if r.Action == "copy" {
						if err := copyTree(related.Source, related.Target); err != nil {
							return err
						}
					} else {
						if err := os.Rename(related.Source, related.Target); err != nil {
							return err
						}
					}
				} else if err != nil {
					return err
				}
				if err := verifyManifest(related.Target, related.Manifest); err != nil {
					return err
				}
			}
		}
		if err := set("published"); err != nil {
			return err
		}
	}
	if e.Status == "published" {
		for _, old := range p.Affected {
			inCopies := false
			for _, copy := range old.Copies {
				if within(ch.Source, copy) {
					inCopies = true
				}
			}
			if !within(ch.Source, old.OutputPath) && !within(ch.Source, old.SourcePath) && !inCopies {
				continue
			}
			l, err := s.Store.MediaLink(ctx, old.RemoteID)
			if err != nil {
				return err
			}
			if r.Action == "copy" {
				newPath := replacePrefix(old.OutputPath, ch.Source, ch.Target)
				if inCopies {
					for _, copy := range old.Copies {
						if within(ch.Source, copy) {
							newPath = replacePrefix(copy, ch.Source, ch.Target)
							break
						}
					}
				}
				found := false
				for _, path := range l.Copies {
					if path == newPath {
						found = true
					}
				}
				if !found {
					l.Copies = append(l.Copies, newPath)
				}
			} else {
				l.SourcePath = replacePrefix(l.SourcePath, ch.Source, ch.Target)
				l.OutputPath = replacePrefix(l.OutputPath, ch.Source, ch.Target)
				l.TitlePath = replacePrefix(l.TitlePath, ch.Source, ch.Target)
				for i, path := range l.Copies {
					l.Copies[i] = replacePrefix(path, ch.Source, ch.Target)
				}
			}
			if err := s.Store.PutLink(ctx, *l); err != nil {
				return err
			}
			m, err := s.Store.GetMediaByRemoteID(ctx, l.RemoteID)
			if err != nil {
				return err
			}
			m.STRMPath = l.OutputPath
			if m.STRMPath == "" {
				m.STRMPath = l.SourcePath
			}
			if err := s.Store.PutMedia(ctx, *m); err != nil {
				return err
			}
			if l.OutputPath != "" {
				rel, _ := filepath.Rel(p.Config.STRMPath, l.OutputPath)
				if err := s.Store.SetLocalOrganization(ctx, l.RemoteID, rel); err != nil {
					return err
				}
			}
		}
		for _, old := range p.Assets {
			relatedTarget := ""
			for _, rel := range ch.Related {
				if old.Path == rel.Source {
					relatedTarget = rel.Target
				}
			}
			if !within(ch.Source, old.Path) && relatedTarget == "" {
				continue
			}
			a := old
			a.Path = replacePrefix(old.Path, ch.Source, ch.Target)
			if relatedTarget != "" {
				a.Path = relatedTarget
			}
			if err := s.Store.PutAsset(ctx, a); err != nil {
				return err
			}
			if r.Action != "copy" {
				if err := s.Store.RemoveAsset(ctx, old.Path); err != nil {
					return err
				}
			}
		}
		if err := set("completed"); err != nil {
			return err
		}
		s.Store.Audit(ctx, "file", fmt.Sprintf("任务 %s %s %s -> %s；关联 %d 项", e.ID, r.Action, ch.Source, ch.Target, len(p.Affected)))
	}
	return nil
}
func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("复制软链接请先纠错为复制模式")
	}
	if targetInfo, err := os.Lstat(target); err == nil && targetInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("复制目标含软链接")
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0o750); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("只支持普通文件")
	}
	if _, err := os.Lstat(target); err == nil {
		a, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if store.ContentHash(a) != store.ContentHash(b) {
			return fmt.Errorf("复制目标内容冲突")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(target), ".copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	if err := output.Chmod(0o640); err != nil {
		output.Close()
		return err
	}
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(output.Name(), target)
}

func removeEmptyTree(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyTree(filepath.Join(root, e.Name()))
		}
	}
	_ = os.Remove(root)
}

func treeManifest(root string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(result) > 10000 {
			return fmt.Errorf("目录操作范围过大")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if e.IsDir() {
			result[rel] = "dir"
			return nil
		}
		if e.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[rel] = "symlink:" + target
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return fmt.Errorf("仅支持 STRM 及小型元数据文件")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = store.ContentHash(data)
		return nil
	})
	return result, err
}
func verifyManifest(root string, want map[string]string) error {
	actual, err := treeManifest(root)
	if err != nil {
		return err
	}
	if len(actual) != len(want) {
		return fmt.Errorf("文件操作范围已变化")
	}
	for path, hash := range want {
		if actual[path] != hash {
			return fmt.Errorf("文件内容已变化: %s", path)
		}
	}
	return nil
}
func (s *Service) verifyCloudOperation(ctx context.Context, p FilePlan) error {
	r := p.Request
	ch := p.Changes[0]
	entries := map[string]pan115.Entry{}
	if r.Action == "rename" || r.Action == "move" {
		if err := s.cloudTree(ctx, p.Config.LibraryCID, entries); err != nil {
			return err
		}
	}
	if r.Action == "rename" {
		if e, ok := entries[ch.Source]; ok && e.Name == ch.Target {
			return nil
		}
	} else if r.Action == "move" {
		if e, ok := entries[ch.Source]; ok && e.ParentID == ch.Target {
			return nil
		}
	} else {
		before := map[string]bool{}
		for _, id := range ch.Before {
			before[id] = true
		}
		parent := ch.Target
		name := ch.Name
		if r.Action == "mkdir" {
			parent = ch.Source
			name = ch.Target
		}
		children, err := s.Pan.List(ctx, parent)
		if err != nil {
			return err
		}
		count := 0
		for _, e := range children {
			if !before[e.ID] && e.Name == name && (r.Action != "mkdir" || e.Directory) {
				count++
			}
		}
		if count == 1 {
			return nil
		}
	}
	return fmt.Errorf("115操作结果尚未确认；保留步骤，核对后重试仅检查，不重复修改")
}
