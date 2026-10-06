package organize

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

func TestAnimationRuleMigrationPreservesCustomOrder(t *testing.T) {
	defaults := DefaultClassification()
	legacy := defaults
	legacy.Rules = legacy.Rules[1:]
	upgraded := upgradeDefaultClassification(legacy)
	_, sub, _ := upgraded.Match(&tmdb.Details{Kind: "movie", OriginalLanguage: "zh", GenreIDs: []int{16}})
	if sub != "动画电影" || len(upgraded.Rules) != len(defaults.Rules) {
		t.Fatalf("legacy animation upgrade: %+v", upgraded)
	}
	custom := legacy
	custom.Rules = append([]ClassificationRule{{Name: "自定义动画", Enabled: true, Kind: "movie", Target: "动画专区", Genres: []int{16}}}, custom.Rules...)
	_, sub, _ = upgradeDefaultClassification(custom).Match(&tmdb.Details{Kind: "movie", OriginalLanguage: "zh", GenreIDs: []int{16}})
	if sub != "动画专区" {
		t.Fatal("custom rule order overwritten")
	}
}

func TestUploadedMovieArchiveAndRecoveryKeepPlaybackAndReception(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{true: "lost-copy-response", false: "normal"}[interrupted], func(t *testing.T) {
			s, c, base := modernFixture(t)
			ctx := context.Background()
			pan := &copyingPan{reconcilePan: base, copyError: interrupted}
			s.Pan = pan
			name := "Renegade.Immortal.2026.2160p.mkv"
			base.children["inbox"] = append(base.children["inbox"], pan115.Entry{ID: "uploaded", ParentID: "inbox", Name: name, Size: 123, SHA1: "ABC", PickCode: "pick-original"})
			task := &store.UploadTask{ID: "upload-task", RemoteID: "uploaded", Filename: name, Size: 123, SHA1: "ABC", Status: "completed", TargetCID: "inbox"}
			if err := s.Store.CreateUploadTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := s.IngestUploaded(ctx, &pan115.UploadResult{RemoteID: "uploaded", PickCode: "pick-original", SHA1: "ABC", Size: 123}, name); err != nil {
				t.Fatal(err)
			}
			before, _ := s.Store.GetMediaByRemoteID(ctx, "uploaded")
			old := before.STRMPath
			jobs, err := s.Store.OrganizationJobs(ctx)
			if err != nil || len(jobs) != 1 || !strings.HasPrefix(jobs[0].Title, "Renegade.Immortal") {
				t.Fatalf("wrong upload recognition title: %+v %v", jobs, err)
			}
			var ids []string
			_ = json.Unmarshal(jobs[0].Expected, &ids)
			if len(ids) != 1 || ids[0] != "uploaded" {
				t.Fatal("upload recognition combined files")
			}
			d := &tmdb.Details{ID: 1599191, Kind: "movie", Title: "仙逆剧场版：弑仙之战", Year: 2026, OriginalLanguage: "zh", GenreIDs: []int{16}}
			plan, err := s.buildPlan(ctx, c, OrganizeRequest{IDs: ids, Kind: "movie", TMDBID: d.ID, Manual: true, Mode: "hardlink"}, d)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(c.PendingPath, "电影", "动画电影", "Renegade.Immortal.2026.2160p.strm")
			if plan.Items[0].Link.SourcePath != want || !strings.Contains(plan.Items[0].Link.OutputPath, "电影/动画电影") || pan.copies != 0 {
				t.Fatal("preview mutated or path incorrect")
			}
			_, err = s.executePlan(ctx, c, jobs[0].ID, plan)
			if interrupted {
				if err == nil || pan.copies != 1 {
					t.Fatal("uncertain copy failure not persisted")
				}
				if err := s.Recover(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = s.executePlan(ctx, c, jobs[0].ID, plan); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if pan.copies != 1 {
				t.Fatal("duplicate classification copy")
			}
			m, err := s.Store.GetMediaByRemoteID(ctx, "copy-uploaded")
			if err != nil || m.ID != before.ID || m.PickCode != "pick-original" {
				t.Fatalf("playback identity lost: %+v %v", m, err)
			}
			l, _ := s.Store.MediaLink(ctx, m.RemoteID)
			if l.InboxID != "uploaded" || l.SourcePath != want {
				t.Fatal("source association incorrect")
			}
			if _, err := os.Stat(old); !os.IsNotExist(err) {
				t.Fatal("old upload STRM retained")
			}
			a, _ := os.Stat(want)
			b, _ := os.Stat(l.OutputPath)
			if a == nil || b == nil || !os.SameFile(a, b) {
				t.Fatal("hardlink not published")
			}
			if len(base.children["inbox"]) != 2 {
				t.Fatal("reception original removed")
			}
			if err := s.deleteMediaLocked(ctx, c, m.RemoteID, "chain", "test-chain"); err != nil {
				t.Fatal(err)
			}
			for _, id := range base.deletes {
				if id == "uploaded" {
					t.Fatal("chain deletion removed reception original")
				}
			}
		})
	}
}
