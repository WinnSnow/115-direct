package organize

import (
	"context"
	"fmt"
	"testing"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type copyingPan struct {
	*reconcilePan
	copies    int
	copyError bool
}

func (p *copyingPan) Copy(_ context.Context, target string, ids ...string) error {
	p.copies++
	for _, id := range ids {
		var source pan115.Entry
		for _, entries := range p.children {
			for _, entry := range entries {
				if entry.ID == id {
					source = entry
				}
			}
		}
		if source.ID == "" {
			return fmt.Errorf("source not found")
		}
		copy := source
		copy.ID = "copy-" + source.ID
		copy.ParentID = target
		p.children[target] = append(p.children[target], copy)
		p.copyChildren(source.ID, copy.ID)
	}
	if p.copyError {
		return fmt.Errorf("response interrupted after copy")
	}
	return nil
}

func (p *copyingPan) copyChildren(source, target string) {
	for _, entry := range p.children[source] {
		child := entry
		child.ID = "copy-" + entry.ID
		child.ParentID = target
		p.children[target] = append(p.children[target], child)
		if entry.Directory {
			p.copyChildren(entry.ID, child.ID)
		}
	}
}

func TestPlacementCopiesWholeDirectoryOnceWithoutRenamingOrDeleting(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			ctx := context.Background()
			st := reconcileStore(t)
			pan := &copyingPan{reconcilePan: &reconcilePan{children: map[string][]pan115.Entry{
				"inbox":  {{ID: "stage", Name: "Original release [接收中-job]", Directory: true}},
				"stage":  {{ID: "source", Name: "Original release", Directory: true}},
				"source": {{ID: "file", Name: "Original.1999.mkv", PickCode: "pick", SHA1: "sha", Size: 123}},
			}}, copyError: interrupted}
			s := NewService(st, pan, nil, nil, DirectoryConfig{})
			cfg := DirectoryConfig{InboxCID: "inbox", LibraryCID: "library"}
			job := &store.TransferJob{ID: "job", StageCID: "stage"}
			details := &tmdb.Details{Kind: "movie", ID: 603, Title: "Matrix", OriginalLanguage: "en"}
			files, err := s.placeReceived(ctx, cfg, job, details)
			if interrupted {
				if err == nil {
					t.Fatal("missing copy error")
				}
				files, err = s.placeReceived(ctx, cfg, job, details)
			}
			if err != nil || len(files) != 1 || files[0].Entry.Name != "Original.1999.mkv" {
				t.Fatalf("placement failed: %#v %v", files, err)
			}
			if _, err := s.placeReceived(ctx, cfg, job, details); err != nil {
				t.Fatal(err)
			}
			if pan.copies != 1 || len(pan.renames) != 0 || len(pan.moves) != 0 || len(pan.deletes) != 0 {
				t.Fatalf("unexpected writes %#v", pan)
			}
			if len(pan.children["source"]) != 1 || pan.children["source"][0].Name != "Original.1999.mkv" {
				t.Fatal("source changed")
			}
		})
	}
}

func TestPlacementPreservesWorkFolderWithSingleSeason(t *testing.T) {
	ctx := context.Background()
	pan := &copyingPan{reconcilePan: &reconcilePan{children: map[string][]pan115.Entry{
		"inbox":  {{ID: "work", Name: "Show (2023) {tmdbid-229192}", Directory: true}},
		"work":   {{ID: "season", Name: "Season 01", Directory: true}},
		"season": {{ID: "file", Name: "Show.S01E01.mp4", Size: 42, SHA1: "sha"}},
	}}}
	s := NewService(reconcileStore(t), pan, nil, nil, DirectoryConfig{})
	c := DirectoryConfig{InboxCID: "inbox", LibraryCID: "library"}
	j := &store.TransferJob{ID: "existing-work", StageCID: "work"}
	d := &tmdb.Details{Kind: "tv", ID: 229192, OriginalLanguage: "zh", GenreIDs: []int{16}}
	if _, err := s.placeReceived(ctx, c, j, d); err != nil {
		t.Fatal(err)
	}
	var saved placement
	if err := s.Store.GetSetting(ctx, "placement:"+j.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.CID != "copy-work" || saved.Path != "电视剧/国漫/Show (2023) {tmdbid-229192}" {
		t.Fatalf("work folder lost: %+v", saved)
	}
	if pan.copies != 1 || len(pan.children["copy-work"]) != 1 || pan.children["copy-work"][0].Name != "Season 01" {
		t.Fatal("season hierarchy not preserved")
	}
}

type delayedListingPan struct {
	*copyingPan
	reads int
}

func (p *delayedListingPan) List(ctx context.Context, cid string) ([]pan115.Entry, error) {
	if cid == "copy-source" {
		p.reads++
		if p.reads == 1 {
			return nil, nil
		}
	}
	return p.copyingPan.List(ctx, cid)
}

func TestPlacementWaitsForDirectoryContentsAndDoesNotRecopy(t *testing.T) {
	ctx := context.Background()
	pan := &delayedListingPan{copyingPan: &copyingPan{reconcilePan: &reconcilePan{children: map[string][]pan115.Entry{
		"inbox":  {{ID: "stage", Name: "Film [接收中-job]", Directory: true}},
		"stage":  {{ID: "source", Name: "Film", Directory: true}},
		"source": {{ID: "video", Name: "Film.mkv", Size: 123, SHA1: "sha"}},
	}}}}
	s := NewService(reconcileStore(t), pan, nil, nil, DirectoryConfig{})
	c := DirectoryConfig{InboxCID: "inbox", LibraryCID: "library"}
	j := &store.TransferJob{ID: "job", StageCID: "stage"}
	d := &tmdb.Details{Kind: "movie", OriginalLanguage: "en"}
	files, err := s.placeReceived(ctx, c, j, d)
	if err != nil || len(files) != 1 || pan.reads < 2 {
		t.Fatalf("%+v reads=%d err=%v", files, pan.reads, err)
	}
	if _, err := s.placeReceived(ctx, c, j, d); err != nil {
		t.Fatal(err)
	}
	if pan.copies != 1 {
		t.Fatalf("copies=%d", pan.copies)
	}
}
