package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

type treePan struct{ children map[string][]pan115.Entry }

func (f *treePan) SetCookie(string) error                                   { return nil }
func (f *treePan) Check(context.Context) error                              { return nil }
func (f *treePan) StartQR(context.Context) (*pan115.QRSession, error)       { return nil, nil }
func (f *treePan) PollQR(context.Context, string) (*pan115.QRStatus, error) { return nil, nil }
func (f *treePan) List(_ context.Context, id string) ([]pan115.Entry, error) {
	return f.children[id], nil
}
func (f *treePan) Mkdir(context.Context, string, string) (string, error) { return "", nil }
func (f *treePan) SnapshotShare(context.Context, string, string) (*pan115.ShareSnapshot, error) {
	return nil, nil
}
func (f *treePan) ReceiveShare(context.Context, *pan115.ShareSnapshot, string, string) error {
	return nil
}
func (f *treePan) Copy(context.Context, string, ...string) error { return nil }
func (f *treePan) Move(context.Context, string, ...string) error { return nil }
func (f *treePan) Rename(context.Context, string, string) error  { return nil }
func (f *treePan) Delete(context.Context, ...string) error       { return nil }
func (f *treePan) DownloadURL(context.Context, string, string) (string, map[string]string, error) {
	return "", nil, nil
}

func syncStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSyncCreatesSTRMAndOnlyFullRunsCountMissingFiles(t *testing.T) {
	ctx := context.Background()
	st := syncStore(t)
	root := t.TempDir()
	if err := st.PutSetting(ctx, "directories", organize.DirectoryConfig{LibraryCID: "library", STRMPath: root, GatewayURL: "https://media.example"}); err != nil {
		t.Fatal(err)
	}
	pan := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "movies", Name: "电影", Directory: true}}, "movies": {{ID: "file", Name: "Movie (2024).mkv", PickCode: "pc", SHA1: "sha"}}}}
	service := New(st, pan, []byte("01234567890123456789012345678901"), organize.DirectoryConfig{})
	if err := service.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	media, err := st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatal("STRM missing")
	}
	content, _ := os.ReadFile(media.STRMPath)
	if !strings.Contains(string(content), "sig=") || !strings.Contains(string(content), "jellyfin_sig=") {
		t.Fatalf("STRM signatures missing: %s", content)
	}
	pan.children["movies"] = nil
	if err := service.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatal("removed by incremental sync")
	}
	stored, err := st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Missing != 0 {
		t.Fatalf("incremental sync changed missing count to %d", stored.Missing)
	}
	if err := service.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatal("removed after first full pass")
	}
	stored, err = st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Missing != 1 {
		t.Fatalf("first full sync set missing count to %d", stored.Missing)
	}
	if err := service.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Missing != 1 {
		t.Fatalf("incremental sync changed missing count to %d", stored.Missing)
	}
	if err := service.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatalf("missing source deleted local STRM: %v", err)
	}
	if _, err := st.GetMediaByRemoteID(ctx, "file"); err != nil {
		t.Fatal("mapping discarded", err)
	}
	link, err := st.MediaLink(ctx, "file")
	if err != nil || !link.Unavailable {
		t.Fatalf("source not marked unavailable: %+v %v", link, err)
	}
	reviews, err := st.Reviews(ctx)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("missing review: %+v %v", reviews, err)
	}
}

func TestSyncMovesSTRMWhenRemotePathChanges(t *testing.T) {
	ctx := context.Background()
	st := syncStore(t)
	root := t.TempDir()
	if err := st.PutSetting(ctx, "directories", organize.DirectoryConfig{LibraryCID: "library", STRMPath: root, GatewayURL: "https://media.example"}); err != nil {
		t.Fatal(err)
	}
	pan := &treePan{children: map[string][]pan115.Entry{
		"library": {{ID: "movies", Name: "电影", Directory: true}},
		"movies":  {{ID: "file", Name: "Movie (2024).mkv", PickCode: "pc", SHA1: "sha"}},
	}}
	service := New(st, pan, []byte("01234567890123456789012345678901"), organize.DirectoryConfig{})
	if err := service.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	media, err := st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	oldPath := media.STRMPath
	pan.children["movies"] = []pan115.Entry{{ID: "western", Name: "欧美电影", Directory: true}}
	pan.children["western"] = []pan115.Entry{{ID: "file", Name: "Movie (2024).mkv", PickCode: "pc", SHA1: "sha"}}
	if err := service.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	media, err = st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if media.STRMPath == oldPath {
		t.Fatalf("STRM path did not change: %s", oldPath)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old STRM still exists: %v", err)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatalf("new STRM missing: %v", err)
	}
}

func TestIndependentPendingPreservesManualPathsAndDeletionMarks(t *testing.T) {
	ctx := context.Background()
	st := syncStore(t)
	c := organize.DirectoryConfig{LibraryCID: "library", STRMPath: t.TempDir(), PendingPath: t.TempDir(), GatewayURL: "https://gateway"}
	pan := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "one", Name: "Film.1080p.mkv", PickCode: "pick"}}}}
	s := New(st, pan, []byte("secret"), c)
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := st.MediaLink(ctx, "one")
	if err != nil || !strings.HasPrefix(l.SourcePath, c.PendingPath) || l.OutputPath != "" {
		t.Fatal("not pending", l, err)
	}
	content, err := os.ReadFile(l.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	l.OutputPath = filepath.Join(c.STRMPath, "manual.strm")
	l.Manual = true
	_ = os.WriteFile(l.OutputPath, content, 0640)
	_ = st.PutLink(ctx, *l)
	pan.children["library"][0].Name = "Remote.Renamed.mkv"
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := st.MediaLink(ctx, "one")
	if current.OutputPath != l.OutputPath || current.SourcePath != l.SourcePath {
		t.Fatal("manual path changed", current)
	}
	_ = os.Remove(l.OutputPath)
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.OutputPath); !os.IsNotExist(err) {
		t.Fatal("external missing rebuilt")
	}
	reviews, _ := st.Reviews(ctx)
	if len(reviews) != 1 || reviews[0].Status != "pending" {
		t.Fatal("not queued", reviews)
	}
	l.Deleted = "output"
	_ = st.PutLink(ctx, *l)
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.OutputPath); !os.IsNotExist(err) {
		t.Fatal("tombstone ignored")
	}
	pan.children["library"] = nil
	for i := 0; i < 3; i++ {
		if err := s.RunFull(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(l.SourcePath); err != nil {
		t.Fatal("missing cloud removed source", err)
	}
	current, _ = st.MediaLink(ctx, "one")
	if !current.Unavailable || current.Deleted != "output" {
		t.Fatal("state discarded", current)
	}
}

func TestFullSyncDoesNotQueueMissingReviewsForExplicitDeletions(t *testing.T) {
	for _, policy := range []string{"output", "local", "chain"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			st := syncStore(t)
			cfg := organize.DirectoryConfig{LibraryCID: "library", STRMPath: t.TempDir(), PendingPath: t.TempDir(), GatewayURL: "https://gateway"}
			pan := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "one", Name: "Film.mkv", PickCode: "pick"}}}}
			s := New(st, pan, []byte("secret"), cfg)
			if err := s.RunFull(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetMediaByRemoteID(ctx, "one")
			if err != nil {
				t.Fatal(err)
			}
			link, err := st.MediaLink(ctx, "one")
			if err != nil {
				t.Fatal(err)
			}
			link.Deleted = policy
			if err := st.PutLink(ctx, *link); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(link.SourcePath); err != nil {
				t.Fatal(err)
			}
			pan.children["library"] = nil
			for i := 0; i < 2; i++ {
				if err := s.RunFull(ctx); err != nil {
					t.Fatal(err)
				}
			}
			current, err := st.MediaLink(ctx, "one")
			if err != nil || current.Deleted != policy || !current.Unavailable || current.SourcePath != link.SourcePath {
				t.Fatalf("deletion mapping changed: %+v %v", current, err)
			}
			after, err := st.GetMediaByRemoteID(ctx, "one")
			if err != nil || after.ID != before.ID {
				t.Fatalf("playback identity changed: %+v %v", after, err)
			}
			reviews, err := st.Reviews(ctx)
			if err != nil || len(reviews) != 0 {
				t.Fatalf("explicit deletion queued as uncertain: %+v %v", reviews, err)
			}
			if _, err := os.Stat(link.SourcePath); !os.IsNotExist(err) {
				t.Fatalf("deleted STRM rebuilt: %v", err)
			}
		})
	}
}

func TestSyncPreservesLocalOrganizationWhenRemoteNamesChange(t *testing.T) {
	ctx := context.Background()
	st := syncStore(t)
	root := t.TempDir()
	cfg := organize.DirectoryConfig{LibraryCID: "library", STRMPath: root, GatewayURL: "https://gateway"}
	if err := st.PutSetting(ctx, "directories", cfg); err != nil {
		t.Fatal(err)
	}
	pan := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "file", Name: "Original.mkv", PickCode: "pick"}}}}
	s := New(st, pan, []byte("secret"), cfg)
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	media, err := st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(media.STRMPath, "待整理") {
		t.Fatal(media.STRMPath)
	}
	rel := filepath.Join("电影", "欧美电影", "Title (2024) [tmdbid-603]", "Title.strm")
	if err := st.SetLocalOrganization(ctx, "file", rel); err != nil {
		t.Fatal(err)
	}
	pan.children["library"][0].Name = "Renamed.mkv"
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	media, err = st.GetMediaByRemoteID(ctx, "file")
	if err != nil {
		t.Fatal(err)
	}
	if media.STRMPath != filepath.Join(root, rel) || media.Name != "Renamed.mkv" {
		t.Fatalf("local path overwritten: %#v", media)
	}
	if _, err := os.Stat(media.STRMPath); err != nil {
		t.Fatal(err)
	}
}

func TestFullSyncDoesNotMarkUploadSourceMissing(t *testing.T) {
	ctx := context.Background()
	st := syncStore(t)
	root := t.TempDir()
	cfg := organize.DirectoryConfig{LibraryCID: "library", STRMPath: root, GatewayURL: "https://media.example"}
	if err := st.PutSetting(ctx, "directories", cfg); err != nil {
		t.Fatal(err)
	}
	m := store.MediaEntry{ID: "upload-media", RemoteID: "upload-remote", Name: "upload.mkv", RemotePath: "上传/upload.mkv", STRMPath: filepath.Join(root, "upload.strm")}
	if err := st.PutMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLink(ctx, store.MediaLink{RemoteID: m.RemoteID, SourcePath: m.STRMPath, Mode: "upload"}); err != nil {
		t.Fatal(err)
	}
	service := New(st, &treePan{children: map[string][]pan115.Entry{"library": nil}}, []byte("01234567890123456789012345678901"), organize.DirectoryConfig{})
	if err := service.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetMediaByRemoteID(ctx, m.RemoteID)
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.MediaLink(ctx, m.RemoteID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Missing != 0 || link.Unavailable {
		t.Fatalf("upload source marked missing: media=%+v link=%+v", stored, link)
	}
}
