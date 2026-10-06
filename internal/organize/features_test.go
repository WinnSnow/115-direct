package organize

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func TestClassificationOrderedRulesAndCustomRoots(t *testing.T) {
	c := DefaultClassification()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		d         tmdb.Details
		root, sub string
	}{
		{tmdb.Details{Kind: "movie", OriginalLanguage: "zh"}, "电影", "华语电影"},
		{tmdb.Details{Kind: "tv", OriginCountries: []string{"JP"}, GenreIDs: []int{16}}, "电视剧", "日番"},
		{tmdb.Details{Kind: "tv", OriginCountries: []string{"CN"}, GenreIDs: []int{99}}, "电视剧", "纪录片"},
		{tmdb.Details{Kind: "movie", OriginalLanguage: "xx"}, "电影", ""},
	} {
		root, sub, _ := c.Match(&test.d)
		if root != test.root || sub != test.sub {
			t.Fatalf("%+v: %s/%s", test.d, root, sub)
		}
	}
	c.MovieRoot = "Films"
	c.Rules = append([]ClassificationRule{{Name: "自定义", Enabled: true, Kind: "movie", Keyword: "Matrix", Target: "科幻"}}, c.Rules...)
	root, sub, _ := c.Match(&tmdb.Details{Kind: "movie", Title: "The Matrix", OriginalLanguage: "en"})
	if root != "Films" || sub != "科幻" {
		t.Fatalf("%s/%s", root, sub)
	}
	c.Rules[0].Target = "../outside"
	if c.Validate() == nil {
		t.Fatal("path escape accepted")
	}
}

func TestReceiveCleanupDefaultsAndValidation(t *testing.T) {
	o := DefaultOptions()
	if o.ReceiveCleanupMode != "disabled" || o.ReceiveCleanupDays != 7 {
		t.Fatalf("unexpected defaults: %+v", o)
	}
	o.ReceiveCleanupMode = "after_days"
	o.ReceiveCleanupDays = 30
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.ReceiveCleanupMode = "hourly"
	if o.Validate() == nil {
		t.Fatal("invalid receive cleanup mode accepted")
	}
	o.ReceiveCleanupMode = "after_days"
	o.ReceiveCleanupDays = 3651
	if o.Validate() == nil {
		t.Fatal("invalid receive cleanup days accepted")
	}
}

func TestIndependentDefaultPoliciesAndBatchSelection(t *testing.T) {
	o := DefaultOptions()
	if o.Policy("tv") != "keep" || o.Policy("movie") != "quality" {
		t.Fatalf("%+v", o)
	}
	for _, test := range []struct{ kind, policy, winner string }{
		{"movie", "quality", "high"}, {"movie", "keep", "low"}, {"movie", "newest", "high"}, {"movie", "overwrite", "high"}, {"movie", "coexist", "both"}, {"tv", "keep", "low"},
	} {
		t.Run(test.kind+"-"+test.policy, func(t *testing.T) {
			s, c, _ := modernFixture(t)
			ctx := context.Background()
			o := DefaultOptions()
			o.MovieVersionPolicy, o.TVVersionPolicy = test.policy, test.policy
			if err := s.Store.PutSetting(ctx, "organization", o); err != nil {
				t.Fatal(err)
			}
			seedModern(t, s, c, "low", "Release.S01E01.1080p.120FPS.DV.mkv")
			seedModern(t, s, c, "high", "Release.S01E01.2160p.mkv")
			id := int64(603)
			if test.kind == "tv" {
				id = 229192
			}
			p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"low", "high"}, Kind: test.kind, TMDBID: id})
			if err != nil {
				t.Fatal(err)
			}
			winners := []string{}
			for _, item := range p.Items {
				if item.Skip == "" {
					winners = append(winners, item.Media.RemoteID)
				}
			}
			want := test.winner
			if want == "both" {
				want = "low,high"
			}
			if strings.Join(winners, ",") != want {
				t.Fatalf("winners=%v plan=%+v", winners, p.Items)
			}
		})
	}
	known := store.MediaLink{Quality: store.Quality{Resolution: 2160}, IngestedAt: time.Now().Add(-time.Hour)}
	unknown := store.MediaLink{IngestedAt: time.Now()}
	if preferVersion("quality", unknown, known) {
		t.Fatal("unknown quality retired known quality")
	}
}

func TestSameNameCloudVersionsPreserveOriginalAndResumeCopy(t *testing.T) {
	ctx := context.Background()
	pan := &copyingPan{reconcilePan: &reconcilePan{children: map[string][]pan115.Entry{
		"inbox":   {{ID: "stage", Name: "Same release [接收中-second]", Directory: true}},
		"stage":   {{ID: "source", Name: "Same release", Directory: true}},
		"source":  {{ID: "file", Name: "Film.2160p.mkv", PickCode: "pick"}},
		"library": {{ID: "movies", Name: "电影", Directory: true}},
		"movies":  {{ID: "western", Name: "欧美电影", Directory: true}},
		"western": {{ID: "old", Name: "Same release", Directory: true}},
	}}, copyError: true}
	s := NewService(reconcileStore(t), pan, nil, nil, DirectoryConfig{})
	c := DirectoryConfig{LibraryCID: "library", InboxCID: "inbox"}
	j := &store.TransferJob{ID: "second", StageCID: "stage"}
	d := &tmdb.Details{Kind: "movie", OriginalLanguage: "en"}
	if _, err := s.placeReceived(ctx, c, j, d); err == nil {
		t.Fatal("missing interrupted result")
	}
	files, err := s.placeReceived(ctx, c, j, d)
	if err != nil || len(files) != 1 || !strings.Contains(filepath.ToSlash(files[0].Relative), "[v-") {
		t.Fatalf("%+v %v", files, err)
	}
	if pan.copies != 1 || len(pan.children["source"]) != 1 || pan.children["western"][0].ID != "old" || len(pan.deletes) != 0 {
		t.Fatal("original or old version changed")
	}
}

func TestExistingTVEpisodeIsNotOverwrittenByHigherResolution(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "old", "Show.S01E01.1080p.mkv")
	p, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"old"}, Kind: "tv", TMDBID: 229192})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.executePlan(ctx, c, "oldjob", p); err != nil {
		t.Fatal(err)
	}
	seedModern(t, s, c, "new", "Show.S01E01.2160p.mkv")
	next, err := s.Preview(ctx, OrganizeRequest{IDs: []string{"new"}, Kind: "tv", TMDBID: 229192})
	if err != nil || next.Items[0].Skip == "" || len(next.Items[0].Retire) != 0 {
		t.Fatalf("%+v %v", next, err)
	}
	if _, err := s.executePlan(ctx, c, "newjob", next); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Store.MediaLink(ctx, "old")
	newLink, _ := s.Store.MediaLink(ctx, "new")
	if old.Deleted != "" || newLink.OutputPath != "" || newLink.Suppressed == "" {
		t.Fatalf("old=%+v new=%+v", old, newLink)
	}
}

func TestManualConfirmationUpdatesOriginalRecognitionHint(t *testing.T) {
	s, _, _ := modernFixture(t)
	ctx := context.Background()
	j := &store.TransferJob{ID: "manual-match", Source: "web", Status: "waiting_match"}
	if err := s.Store.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	j.Title = "Release folder"
	if err := s.Store.UpdateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	hint := tmdb.Hint{Title: "The Matrix", Year: 1999, Kind: "movie"}
	if err := s.Store.PutSetting(ctx, "recognition-hint:"+j.ID, hint); err != nil {
		t.Fatal(err)
	}
	if err := s.Confirm(ctx, j.ID, "movie", 603); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := s.Store.Cached(ctx, matchCacheKey(hint)); err != nil || !hit {
		t.Fatalf("manual result missed original hint: %v %v", hit, err)
	}
}
