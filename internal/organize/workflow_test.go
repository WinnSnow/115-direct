package organize

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func modernFixture(t *testing.T) (*Service, DirectoryConfig, *reconcilePan) {
	t.Helper()
	st := reconcileStore(t)
	c := DirectoryConfig{STRMPath: t.TempDir(), PendingPath: t.TempDir(), GatewayURL: "https://gateway.example", LibraryCID: "library", InboxCID: "inbox"}
	pan := &reconcilePan{children: map[string][]pan115.Entry{"inbox": {{ID: "original", Name: "Original.mkv"}}}}
	s := NewService(st, pan, metadataClient(t), []byte("secret"), c)
	if err := st.PutSetting(context.Background(), "directories", c); err != nil {
		t.Fatal(err)
	}
	return s, c, pan
}
func seedModern(t *testing.T, s *Service, c DirectoryConfig, id, name string) {
	t.Helper()
	ctx := context.Background()
	m := store.MediaEntry{ID: "play-" + id, RemoteID: id, Name: name, PickCode: "pick", SHA1: "sha-" + id, RemotePath: name}
	path := filepath.Join(c.PendingPath, id+".strm")
	m.STRMPath = path
	if err := os.WriteFile(path, []byte(s.mediaContent(c, m)), 0640); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.PutMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.PutLink(ctx, store.MediaLink{RemoteID: id, InboxID: "original", SourcePath: path, IngestedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}
func applyModern(t *testing.T, s *Service, c DirectoryConfig, id, mode, job string) *OrganizePlan {
	t.Helper()
	ctx := context.Background()
	p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{id}, Kind: "movie", TMDBID: 603, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.executePlan(ctx, c, job, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRestrictedTemplatesAndDefaultTVName(t *testing.T) {
	d := &tmdb.Details{Kind: "tv", Title: "春花焰", Year: 2024}
	v := templateValues(d, "Show.S01E32.mkv", Episode{1, 32, 32})
	path, err := RenderTemplate(DefaultTVTemplate, v)
	want := filepath.FromSlash("春花焰 (2024)/Season 01/春花焰 - S01E32 - 第 32 集.strm")
	if err != nil || path != want {
		t.Fatalf("%q %v", path, err)
	}
	for _, bad := range []string{"../{{title}}.strm", "/{{title}}.strm", "{{unknown}}.strm", "{% for item %}x{% endif %}", "{% if year %}x.strm", "{{title.__class__}}.strm", "a//b.strm", "a\\b.strm", "x.txt", "{% endif %}x.strm"} {
		if _, err := RenderTemplate(bad, v); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	delete(v, "season_episode")
	if _, err := RenderTemplate(DefaultTVTemplate, v); err == nil {
		t.Fatal("missing season accepted")
	}
}
func TestQualityKeepsIndependentDimensions(t *testing.T) {
	a := ParseQuality("Film.4K.60FPS.DV.HEVC.Atmos.mkv")
	b := ParseQuality("Film.1080p.120FPS.HDR10.AAC.mkv")
	if a.Resolution != 2160 || a.FPS != "60" || a.HDR != "DV" || a.Codec != "HEVC" || a.Audio != "ATMOS" || b.Resolution != 1080 || ParseQuality("60FPS.DV.mkv").Resolution != 0 {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestFourModesAndIdempotentResume(t *testing.T) {
	for _, mode := range []string{"copy", "hardlink", "symlink", "move"} {
		t.Run(mode, func(t *testing.T) {
			s, c, _ := modernFixture(t)
			seedModern(t, s, c, "one", "The.Matrix.1999.1080p.mkv")
			p := applyModern(t, s, c, "one", mode, "job")
			item := p.Items[0]
			data, err := os.ReadFile(item.Link.OutputPath)
			if err != nil || string(data) != s.mediaContent(c, item.Media) {
				t.Fatal("invalid output", err)
			}
			m, err := s.Store.GetMediaByRemoteID(context.Background(), "one")
			if err != nil || m.ID != "play-one" {
				t.Fatal("playback identity changed", m, err)
			}
			info, err := os.Lstat(item.Link.OutputPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("not a symlink")
			}
			source, err := os.Stat(item.Link.SourcePath)
			if mode == "move" && !os.IsNotExist(err) {
				t.Fatal("move source retained")
			}
			if mode == "hardlink" {
				output, _ := os.Stat(item.Link.OutputPath)
				if err != nil || !os.SameFile(source, output) {
					t.Fatal("not a hardlink")
				}
			}
			if _, err := s.executePlan(context.Background(), c, "job", p); err != nil {
				t.Fatal("retry failed", err)
			}
			steps, err := s.Store.Executions(context.Background())
			if err != nil || len(steps) != 1 || steps[0].Status != "completed" {
				t.Fatal("duplicate execution", steps, err)
			}
		})
	}
}

func TestCloudMatchReuseAndLocalCorrectionKeepSourceIdentity(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	// Local generation must not re-match the confirmed cloud title or modify 115.
	s.Pan, s.TMDB = nil, nil
	rel := filepath.Join("电影", "欧美电影", "Original", "Original.S01E01.1080p.mkv")
	ids, err := s.registerSources(ctx, c, []sourceFile{{Entry: pan115.Entry{ID: "classified", Name: filepath.Base(rel), PickCode: "pick", Size: 1}, Relative: rel}}, "original")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Store.GetMediaByRemoteID(ctx, "classified")
	if err != nil {
		t.Fatal(err)
	}
	job := &store.TransferJob{ID: "cloud-confirmed", Source: "share", Status: "organizing"}
	if err := s.Store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	details := &tmdb.Details{Kind: "movie", ID: 603, Title: "The Matrix", Year: 1999, OriginalLanguage: "en"}
	if err := s.finishModern(ctx, c, job, OrganizeRequest{IDs: ids, Kind: details.Kind, TMDBID: details.ID}, details, func(err error) error { return err }); err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" {
		t.Fatalf("cloud match was not reused: %+v", job)
	}
	link, err := s.Store.MediaLink(ctx, "classified")
	wantSource := filepath.Join(c.PendingPath, strings.TrimSuffix(rel, ".mkv")+".strm")
	if err != nil || link.SourcePath != wantSource || link.InboxID != "original" || link.TMDBID != 603 {
		t.Fatalf("source hierarchy or cloud match changed: %+v %v", link, err)
	}

	// Correct an already completed title locally, then scrape it again.
	s.TMDB = metadataClient(t)
	request := OrganizeRequest{IDs: ids, Kind: "tv", TMDBID: 229192, Offset: 1, Manual: true}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.Preview(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		request.Digest = plan.Digest
		manual, err := s.SubmitPlan(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.process(ctx, manual.ID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.Store.GetMediaByRemoteID(ctx, "classified")
	if err != nil || after.ID != before.ID || after.RemotePath != rel || after.PickCode != before.PickCode || after.Name != before.Name {
		t.Fatalf("local correction changed cloud/playback identity: %+v %v", after, err)
	}
	link, err = s.Store.MediaLink(ctx, "classified")
	if err != nil || !link.Manual || link.TMDBID != 229192 || link.Season != 1 || link.Episode != 2 || link.SourcePath != wantSource || link.InboxID != "original" {
		t.Fatalf("local correction was not preserved: %+v %v", link, err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(after.STRMPath, ".strm") + ".nfo")
	if err != nil {
		t.Fatal(err)
	}
	var nfo metadataNFO
	if err := xml.Unmarshal(data, &nfo); err != nil || nfo.Title != "Episode Two" || nfo.Episode == nil || *nfo.Episode != 2 {
		t.Fatalf("corrected metadata=%+v err=%v", nfo, err)
	}
	content, err := os.ReadFile(after.STRMPath)
	if err != nil || !strings.Contains(string(content), "/direct/"+before.ID+"?sig=") {
		t.Fatalf("playback changed: %s %v", content, err)
	}
	if _, err := os.Stat(wantSource); err != nil {
		t.Fatal("pending source was removed", err)
	}
}

func TestBrowsingRootDoesNotExpandCloudMutationBoundary(t *testing.T) {
	s, _, pan := modernFixture(t)
	pan.children["0"] = []pan115.Entry{{ID: "library", Name: "Classified", Directory: true}, {ID: "inbox", Name: "Received", Directory: true}, {ID: "other", Name: "Other", Directory: true}}
	pan.children["library"] = []pan115.Entry{{ID: "inside", Name: "Inside", Directory: true}}
	for _, cid := range []string{"0", "inbox", "other"} {
		for _, action := range []string{"rename", "copy", "move", "delete"} {
			if _, err := s.PreviewFiles(context.Background(), FileRequest{Scope: "115", Action: action, Paths: []string{cid}, Target: "library"}); err == nil {
				t.Fatalf("allowed %s outside classification root: %s", action, cid)
			}
		}
	}
	if _, err := s.PreviewFiles(context.Background(), FileRequest{Scope: "115", Action: "mkdir", Paths: []string{"library"}, Target: "New folder"}); err != nil {
		t.Fatal("classification root management blocked", err)
	}
	if len(pan.moves) != 0 || len(pan.deletes) != 0 || len(pan.renames) != 0 {
		t.Fatal("preview changed cloud files")
	}
}

type mkdirTestPan struct {
	*reconcilePan
	lists     []string
	blocked   string
	uncertain bool
	creates   int
}

func (p *mkdirTestPan) List(ctx context.Context, id string) ([]pan115.Entry, error) {
	p.lists = append(p.lists, id)
	if id == p.blocked {
		return nil, fmt.Errorf("父目录读取失败")
	}
	return p.reconcilePan.List(ctx, id)
}
func (p *mkdirTestPan) Mkdir(ctx context.Context, parent, name string) (string, error) {
	p.creates++
	id, err := p.reconcilePan.Mkdir(ctx, parent, name)
	if p.uncertain {
		return "", fmt.Errorf("响应中断")
	}
	return id, err
}

func TestCloudMkdirOutsideLibraryDoesNotRequireLocalMounts(t *testing.T) {
	for _, parent := range []string{"0", "inbox", "other", "upload", "library"} {
		t.Run(parent, func(t *testing.T) {
			s, c, pan := modernFixture(t)
			c.PendingPath, c.STRMPath, c.LibraryCID = "/missing/pending", "/missing/output", ""
			if err := s.Store.PutSetting(context.Background(), "directories", c); err != nil {
				t.Fatal(err)
			}
			p := &mkdirTestPan{reconcilePan: pan}
			s.Pan = p
			request := FileRequest{Scope: "115", Action: "mkdir", Paths: []string{parent}, Target: "新建目录"}
			preview, err := s.PreviewFiles(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if p.creates != 0 {
				t.Fatal("preview modified cloud")
			}
			request.Digest = preview.Digest
			items, err := s.ExecuteFiles(context.Background(), request)
			if err != nil || len(items) != 1 || items[0].Status != "completed" {
				t.Fatalf("execution: %#v %v", items, err)
			}
			if p.creates != 1 {
				t.Fatalf("mkdir calls=%d", p.creates)
			}
			for _, id := range p.lists {
				if id != parent {
					t.Fatalf("unnecessary directory scan: %s", id)
				}
			}
		})
	}
}

func TestCloudMkdirValidatesNameParentAndDuplicate(t *testing.T) {
	s, _, pan := modernFixture(t)
	p := &mkdirTestPan{reconcilePan: pan, blocked: "unavailable"}
	s.Pan = p
	pan.children["0"] = []pan115.Entry{{ID: "exists", Name: "已有目录", Directory: true}}
	for _, test := range []struct{ parent, name string }{
		{"0", "已有目录"}, {"0", "../越界"}, {"0", ".."}, {"0", ""}, {"unavailable", "目录"}, {"", "目录"},
	} {
		if _, err := s.PreviewFiles(context.Background(), FileRequest{Scope: "115", Action: "mkdir", Paths: []string{test.parent}, Target: test.name}); err == nil {
			t.Fatalf("accepted invalid request: %+v", test)
		}
	}
	if p.creates != 0 {
		t.Fatal("invalid preview modified cloud")
	}
}

func TestCloudMkdirLostResponseRetryOnlyVerifies(t *testing.T) {
	s, _, pan := modernFixture(t)
	p := &mkdirTestPan{reconcilePan: pan, uncertain: true}
	s.Pan = p
	r := FileRequest{Scope: "115", Action: "mkdir", Paths: []string{"0"}, Target: "上传目录"}
	preview, err := s.PreviewFiles(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Digest = preview.Digest
	items, err := s.ExecuteFiles(context.Background(), r)
	if err != nil || len(items) != 1 || items[0].Status != "remote_pending" || items[0].Error == "" {
		t.Fatalf("uncertain execution: %#v %v", items, err)
	}
	if err := s.RetryExecution(context.Background(), items[0].ID); err != nil {
		t.Fatal(err)
	}
	item, err := s.Store.Execution(context.Background(), items[0].ID)
	if err != nil || item.Status != "completed" || p.creates != 1 {
		t.Fatalf("retry: %#v creates=%d err=%v", item, p.creates, err)
	}
}

func TestModeCorrectionMaterializesSamePath(t *testing.T) {
	s, c, _ := modernFixture(t)
	seedModern(t, s, c, "one", "The.Matrix.1080p.mkv")
	p := applyModern(t, s, c, "one", "symlink", "first")
	path := p.Items[0].Link.OutputPath
	applyModern(t, s, c, "one", "copy", "second")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("symlink not replaced", err)
	}
	if err := os.Remove(p.Items[0].Link.SourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal("copy depends on source", err)
	}
}
func TestThreeDeletePoliciesKeepOriginalAndMapping(t *testing.T) {
	for _, policy := range []string{"output", "local", "chain"} {
		t.Run(policy, func(t *testing.T) {
			s, c, pan := modernFixture(t)
			seedModern(t, s, c, "one", "The.Matrix.1080p.mkv")
			p := applyModern(t, s, c, "one", "copy", "first")
			pan.children["library"] = []pan115.Entry{{ID: "one", Name: "The.Matrix.1080p.mkv"}}
			o := DefaultOptions()
			o.DeletePolicy = policy
			if err := s.Store.PutSetting(context.Background(), "organization", o); err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewDelete(context.Background(), "one")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Delete(context.Background(), "one", preview.Digest); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p.Items[0].Link.OutputPath); !os.IsNotExist(err) {
				t.Fatal("output retained")
			}
			_, err = os.Stat(p.Items[0].Link.SourcePath)
			if policy == "output" && err != nil {
				t.Fatal("source removed")
			}
			if policy != "output" && !os.IsNotExist(err) {
				t.Fatal("source retained")
			}
			if policy == "chain" && (len(pan.deletes) != 1 || pan.deletes[0] != "one") {
				t.Fatal("wrong cloud target", pan.deletes)
			}
			if policy != "chain" && len(pan.deletes) != 0 {
				t.Fatal("unexpected cloud delete")
			}
			if len(pan.children["inbox"]) != 1 {
				t.Fatal("original deleted")
			}
			link, err := s.Store.MediaLink(context.Background(), "one")
			if err != nil || link.Deleted != policy {
				t.Fatal("tombstone missing")
			}
			if _, err := s.Store.GetMediaByRemoteID(context.Background(), "one"); err != nil {
				t.Fatal("mapping deleted")
			}
			if policy == "output" {
				if _, err := s.Preview(context.Background(), OrganizeRequest{IDs: []string{"one"}, Kind: "movie", TMDBID: 603}); err != nil {
					t.Fatal("manual recreation blocked", err)
				}
			}
		})
	}
}

func TestSoftlinkDependencyAndSharedAssets(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	link := p.Items[0].Link
	seedModern(t, s, c, "two", "Film.2160p.mkv")
	other, _ := s.Store.MediaLink(ctx, "two")
	other.SourcePath = link.SourcePath
	other.Mode = "symlink"
	other.OutputPath = filepath.Join(c.STRMPath, "shared.strm")
	if err := os.Symlink(link.SourcePath, other.OutputPath); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.PutLink(ctx, *other); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(c.STRMPath, "poster.jpg")
	if err := s.writeAsset(ctx, c.STRMPath, asset, []byte("image"), []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.DeletePolicy = "local"
	_ = s.Store.PutSetting(ctx, "organization", o)
	preview, err := s.PreviewDelete(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Retained) < 2 {
		t.Fatal("references absent", preview)
	}
	if err := s.Delete(ctx, "one", preview.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(other.OutputPath); err != nil {
		t.Fatal("symlink broken", err)
	}
	if _, err := os.ReadFile(asset); err != nil {
		t.Fatal("shared asset deleted", err)
	}
}
func TestVersionPoliciesAndUnknownQuality(t *testing.T) {
	for _, policy := range []string{"coexist", "overwrite", "newest", "quality"} {
		t.Run(policy, func(t *testing.T) {
			s, c, _ := modernFixture(t)
			ctx := context.Background()
			o := DefaultOptions()
			o.MovieVersionPolicy = policy
			_ = s.Store.PutSetting(ctx, "organization", o)
			seedModern(t, s, c, "old", "Film.1080p.mkv")
			old := applyModern(t, s, c, "old", "copy", "oldjob")
			seedModern(t, s, c, "new", "Film.2160p.60FPS.DV.mkv")
			newPlan := applyModern(t, s, c, "new", "copy", "newjob")
			if old.Items[0].Link.OutputPath == newPlan.Items[0].Link.OutputPath {
				t.Fatal("version collision")
			}
			oldLink, _ := s.Store.MediaLink(ctx, "old")
			if policy == "coexist" && oldLink.Deleted != "" {
				t.Fatal("coexisting version deleted")
			}
			if policy != "coexist" && oldLink.Deleted != "output" {
				t.Fatal("old version not retired")
			}
			if policy == "quality" {
				seedModern(t, s, c, "unknown", "Film.DV.60FPS.mkv")
				p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"unknown"}, Kind: "movie", TMDBID: 603})
				if err != nil || p.Items[0].Skip == "" {
					t.Fatal("unknown replaced known", p, err)
				}
			}
		})
	}
}
func TestMountReplacementStopsWritesAndNestedRootsRejected(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	if err := CheckRoots(ctx, s.Store, c); err != nil {
		t.Fatal(err)
	}
	old := c.PendingPath + "-offline"
	if err := os.Rename(c.PendingPath, old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(c.PendingPath); os.Rename(old, c.PendingPath) })
	if err := os.Mkdir(c.PendingPath, 0750); err != nil {
		t.Fatal(err)
	}
	if err := CheckRoots(ctx, s.Store, c); err == nil {
		t.Fatal("offline mount accepted")
	}
	c.PendingPath = c.STRMPath
	if err := ValidateRoots(c); err == nil {
		t.Fatal("same roots accepted")
	}
}

func TestPublishedStepRecoversMappingAndNeverDuplicates(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"one"}, Kind: "movie", TMDBID: 603})
	if err != nil {
		t.Fatal(err)
	}
	item := p.Items[0]
	if err := EnsureLocalDirectory(c.STRMPath, filepath.Dir(item.Link.OutputPath)); err != nil {
		t.Fatal(err)
	}
	content := s.mediaContent(c, item.Media)
	_ = os.WriteFile(item.Link.OutputPath, []byte(content), 0640)
	raw, _ := json.Marshal(fileStep{c, item, content, "output"})
	e := store.Execution{ID: "organize:crash:one", Kind: "organize", Status: "published", Body: raw}
	if err := s.Store.PutExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Store.GetMediaByRemoteID(ctx, "one")
	if err != nil || m.STRMPath != item.Link.OutputPath {
		t.Fatal("mapping not recovered", m, err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUserMetadataNeverOverwrittenOrDeleted(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	path := filepath.Join(c.STRMPath, "poster.jpg")
	_ = os.WriteFile(path, []byte("user"), 0640)
	if err := s.writeAsset(ctx, c.STRMPath, path, []byte("project"), []string{"one"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "user" {
		t.Fatal("user artwork overwritten")
	}
	nfo := filepath.Join(c.STRMPath, "movie.nfo")
	_ = os.WriteFile(nfo, []byte("<movie><title>User</title></movie>"), 0640)
	if err := s.writeOwnedNFO(ctx, c.STRMPath, nfo, metadataNFO{Title: "Project"}, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(nfo)
	if !strings.Contains(string(data), "User") {
		t.Fatal("user NFO overwritten")
	}
}

func TestDirectoryRenameUpdatesReferencesAndRejectsExternalSymlinks(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	source := filepath.Dir(p.Items[0].Link.OutputPath)
	rel, _ := filepath.Rel(c.STRMPath, source)
	r := FileRequest{Scope: "output", Action: "rename", Paths: []string{rel}, Target: "renamed"}
	preview, err := s.PreviewFiles(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Digest = preview.Digest
	items, err := s.ExecuteFiles(ctx, r)
	if err != nil || items[0].Error != "" {
		t.Fatal(items, err)
	}
	l, _ := s.Store.MediaLink(ctx, "one")
	if !strings.Contains(l.OutputPath, filepath.Join(c.STRMPath, "renamed")) {
		t.Fatal("mapping stale", l)
	}
	if _, err := os.ReadFile(l.OutputPath); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(t.TempDir(), filepath.Join(c.STRMPath, "outside"))
	if _, err := s.PreviewFiles(ctx, FileRequest{Scope: "output", Action: "rename", Paths: []string{"outside"}, Target: "target"}); err == nil {
		t.Fatal("outside symlink accepted")
	}
}
func TestPreviewDigestMustMatch(t *testing.T) {
	s, c, _ := modernFixture(t)
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	r := OrganizeRequest{IDs: []string{"one"}, Kind: "movie", TMDBID: 603}
	if _, err := s.SubmitPlan(context.Background(), r); err == nil {
		t.Fatal("unpreviewed plan accepted")
	}
	p, err := s.Preview(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Digest = p.Digest
	if _, err := s.SubmitPlan(context.Background(), r); err != nil {
		t.Fatal("matching preview rejected", err)
	}
}

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestImageFailureRetriesIndependentlyAndRespectsDeletion(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	dir := filepath.Dir(p.Items[0].Link.OutputPath)
	status := 502
	s.TMDB.ImageHTTP = &http.Client{Transport: imageTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("image bytes"))}, nil
	})}
	s.artwork(ctx, c.STRMPath, dir, "poster.jpg", "/poster.jpg", []string{"one"})
	steps, err := s.Store.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, e := range steps {
		if e.Kind == "image" && e.Error != "" {
			id = e.ID
		}
	}
	if id == "" {
		t.Fatal("image failure not recorded")
	}
	status = 200
	if err := s.RetryExecution(ctx, id); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "poster.jpg")); err != nil || string(data) != "image bytes" {
		t.Fatal("image retry failed", err)
	}
	status = 502
	s.artwork(ctx, c.STRMPath, dir, "fanart.jpg", "/fanart.jpg", []string{"one"})
	steps, _ = s.Store.Executions(ctx)
	id = ""
	for _, e := range steps {
		if e.Kind == "image" && e.Error != "" {
			id = e.ID
		}
	}
	preview, err := s.PreviewDelete(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "one", preview.Digest); err != nil {
		t.Fatal(err)
	}
	status = 200
	if err := s.RetryExecution(ctx, id); err == nil {
		t.Fatal("deleted metadata rebuilt")
	}
}
func TestLegacyMigrationNeverMovesFilesAndIsIdempotent(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	path := filepath.Join(c.STRMPath, "Film (1999) [tmdbid-603]", "Film.strm")
	_ = os.MkdirAll(filepath.Dir(path), 0750)
	m := store.MediaEntry{ID: "stable", RemoteID: "legacy", Name: "Film.1080p.mkv", STRMPath: path}
	_ = s.Store.PutMedia(ctx, m)
	_ = os.WriteFile(path, []byte(s.mediaContent(c, m)), 0640)
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "movie.nfo"), []byte("<movie><generator>115 Direct</generator><title>Film</title></movie>"), 0640)
	if err := s.MigrateLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateLinks(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := s.Store.MediaLink(ctx, "legacy")
	if err != nil || l.OutputPath != path || l.TMDBID != 603 || !l.Manual {
		t.Fatal("wrong migration", l, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("old file moved")
	}
	m2, _ := s.Store.GetMediaByRemoteID(ctx, "legacy")
	if m2.ID != "stable" {
		t.Fatal("ID changed")
	}
	a, err := s.Store.Asset(ctx, filepath.Join(filepath.Dir(path), "movie.nfo"))
	if err != nil || len(a.Owners) != 1 {
		t.Fatal("legacy NFO association wrong", a, err)
	}
}

func TestManualReorganizeAfterOutputDeleteRegeneratesMetadata(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "Film.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	item := p.Items[0]
	if err := s.writeMetadata(ctx, c, p.Details, []store.MediaEntry{item.Media}); err != nil {
		t.Fatal(err)
	}
	nfo := strings.TrimSuffix(item.Link.OutputPath, ".strm") + ".nfo"
	if _, err := os.Stat(nfo); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewDelete(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "one", preview.Digest); err != nil {
		t.Fatal(err)
	}
	p = applyModern(t, s, c, "one", "copy", "second")
	if err := s.writeMetadata(ctx, c, p.Details, []store.MediaEntry{p.Items[0].Media}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nfo); err != nil {
		t.Fatal("completed asset step prevented explicit regeneration", err)
	}
}

type uncertainCopyPan struct {
	*reconcilePan
	calls int
}

func (p *uncertainCopyPan) Copy(ctx context.Context, target string, ids ...string) error {
	p.calls++
	p.children[target] = append(p.children[target], pan115.Entry{ID: "copy", Name: "Film.mkv", ParentID: target})
	return fmt.Errorf("response lost after successful copy")
}
func TestUncertainCloudCopyVerifiesBeforeRetryWithoutDuplicate(t *testing.T) {
	s, _, base := modernFixture(t)
	ctx := context.Background()
	base.children["library"] = []pan115.Entry{{ID: "one", ParentID: "library", Name: "Film.mkv"}, {ID: "dest", ParentID: "library", Name: "Destination", Directory: true}}
	pan := &uncertainCopyPan{reconcilePan: base}
	s.Pan = pan
	r := FileRequest{Scope: "115", Action: "copy", Paths: []string{"one"}, Target: "dest"}
	p, err := s.PreviewFiles(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Digest = p.Digest
	items, err := s.ExecuteFiles(ctx, r)
	if err != nil || len(items) != 1 || items[0].Status != "remote_pending" || items[0].Error == "" {
		t.Fatal("uncertain result not persisted", items, err)
	}
	if err := s.RetryExecution(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if pan.calls != 1 || len(pan.children["dest"]) != 1 {
		t.Fatal("copy was repeated")
	}
}

func TestVersionCleanupFailureRetryKeepsPublishedVersion(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	o := DefaultOptions()
	o.MovieVersionPolicy = "quality"
	_ = s.Store.PutSetting(ctx, "organization", o)
	seedModern(t, s, c, "old", "Film.1080p.mkv")
	old := applyModern(t, s, c, "old", "copy", "first")
	oldItem := old.Items[0]
	seedModern(t, s, c, "new", "Film.2160p.mkv")
	p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"new"}, Kind: "movie", TMDBID: 603})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(oldItem.Link.OutputPath, []byte("user edited"), 0640)
	if _, err := s.executePlan(ctx, c, "second", p); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	output := p.Items[0].Link.OutputPath
	before, err := os.Stat(output)
	if err != nil {
		t.Fatal("new version not published", err)
	}
	_ = os.WriteFile(oldItem.Link.OutputPath, []byte(s.mediaContent(c, oldItem.Media)), 0640)
	if _, err := s.executePlan(ctx, c, "second", p); err != nil {
		t.Fatal("cleanup retry failed", err)
	}
	after, err := os.Stat(output)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("retry regenerated new version", err)
	}
	if _, err := os.Stat(oldItem.Link.OutputPath); !os.IsNotExist(err) {
		t.Fatal("old version retained")
	}
}
