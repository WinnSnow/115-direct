package organize

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type shareUpdatesPan struct {
	*copyingPan
	snapshot    pan115.ShareSnapshot
	checks      int
	received    []string
	interrupted bool
	checkError  bool
}

func (p *shareUpdatesPan) SnapshotShareTree(_ context.Context, _ string, code string) (*pan115.ShareSnapshot, error) {
	p.checks++
	if p.checkError {
		return nil, fmt.Errorf("snapshot offline")
	}
	if code != "fx1234" {
		return nil, fmt.Errorf("wrong password")
	}
	result := p.snapshot
	result.Entries = append([]pan115.Entry(nil), p.snapshot.Entries...)
	result.Files = append([]pan115.ShareFile(nil), p.snapshot.Files...)
	return &result, nil
}
func (p *shareUpdatesPan) ReceiveShare(_ context.Context, snapshot *pan115.ShareSnapshot, code, target string) error {
	if code != "fx1234" {
		return fmt.Errorf("wrong receive password")
	}
	for _, entry := range snapshot.Entries {
		p.received = append(p.received, entry.ID)
		entry.ID = "receive-" + target + "-" + entry.ID
		entry.ParentID = target
		p.children[target] = append(p.children[target], entry)
	}
	if p.interrupted {
		return fmt.Errorf("connection interrupted after receive")
	}
	return nil
}
func shareEpisode(number int) pan115.ShareFile {
	name := fmt.Sprintf("Show.S01E%02d.1080p.mkv", number)
	return pan115.ShareFile{Relative: "Show/Season 01/" + name, Entry: pan115.Entry{ID: fmt.Sprint("share-file-", number), Name: name, SHA1: fmt.Sprint("sha-", number), PickCode: "pick", Size: 100 + int64(number)}}
}
func shareUpdatesFixture(t *testing.T) (*Service, *shareUpdatesPan) {
	s, _, r := modernFixture(t)
	p := &shareUpdatesPan{copyingPan: &copyingPan{reconcilePan: r}, snapshot: pan115.ShareSnapshot{Code: "share-code", ReceiveCode: "fx1234", Title: "Show", Entries: []pan115.Entry{{ID: "share-work", Name: "Show", Directory: true}}, Files: []pan115.ShareFile{shareEpisode(1)}}}
	s.Pan = p
	return s, p
}
func submitMessage(t *testing.T, s *Service, options ShareSubmissionOptions) *store.TransferJob {
	t.Helper()
	j, err := s.SubmitShareMessage(context.Background(), "user1", "https://115.com/s/share-code", "fx1234", "tv", 229192, options)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func submitMessageLink(t *testing.T, s *Service, link string, options ShareSubmissionOptions) *store.TransferJob {
	t.Helper()
	j, err := s.SubmitShareMessage(context.Background(), "user1", link, "fx1234", "tv", 229192, options)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func completeSubmitted(t *testing.T, s *Service, j *store.TransferJob) {
	t.Helper()
	if err := s.process(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.GetJob(context.Background(), j.ID)
	if err != nil || got.Status != "completed" {
		t.Fatalf("job %s: %+v %v", j.ID, got, err)
	}
}

func TestShareUpdatesFirstDuplicateNewEpisodeAndPreservedFiles(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	options := ShareSubmissionOptions{CheckUpdates: true}
	notifications := 0
	s.Notify = func(_ context.Context, user, text string) {
		if user != "user1" || !strings.Contains(text, "整理完成") {
			t.Fatal("incorrect completion notification")
		}
		notifications++
	}
	first := submitMessage(t, s, options)
	completeSubmitted(t, s, first)
	media, _ := s.Store.ListMedia(context.Background())
	if len(media) != 1 {
		t.Fatal("first media missing")
	}
	old := media[0]
	before, err := os.ReadFile(old.STRMPath)
	if err != nil {
		t.Fatal(err)
	}
	second := submitMessage(t, s, options)
	if !second.Duplicate || second.ID != first.ID || !strings.Contains(second.SubmissionMessage, "已转存") || len(p.received) != 1 {
		t.Fatal("unchanged share received again")
	}
	p.snapshot.Files = append(p.snapshot.Files, shareEpisode(2)) // root folder ID/name stays unchanged
	updated := submitMessage(t, s, options)
	if updated.Duplicate || !updated.ShareUpdate || updated.ID == first.ID {
		t.Fatal("nested new episode not detected")
	}
	completeSubmitted(t, s, updated)
	if len(p.received) != 2 || p.received[1] != shareEpisode(2).Entry.ID {
		t.Fatal("old episodes received again", p.received)
	}
	if notifications != 2 {
		t.Fatal("completion notification missing")
	}
	after, _ := os.ReadFile(old.STRMPath)
	if string(before) != string(after) {
		t.Fatal("old STRM changed")
	}
	oldAfter, _ := s.Store.GetMedia(context.Background(), old.ID)
	if oldAfter.STRMPath != old.STRMPath {
		t.Fatal("old playback identity changed")
	}
	media, _ = s.Store.ListMedia(context.Background())
	if len(media) != 2 {
		t.Fatal("new episode not organized")
	}
	third := submitMessage(t, s, options)
	if !third.Duplicate || third.ID != updated.ID {
		t.Fatal("updated revision not deduplicated")
	}
	// Removing a source file does not remove previously received files or outputs.
	p.snapshot.Files = p.snapshot.Files[1:]
	removed := submitMessage(t, s, options)
	if !removed.Duplicate || len(p.received) != 2 || len(p.deletes) != 0 {
		t.Fatal("share removal triggered receive/delete")
	}
}

func TestShareUpdatesDifferentShareCodeUsesTMDBBaseline(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	options := ShareSubmissionOptions{CheckUpdates: true}
	first := submitMessageLink(t, s, "https://115.com/s/old-share-code", options)
	completeSubmitted(t, s, first)
	p.snapshot.Files = append(p.snapshot.Files, shareEpisode(2))
	updated := submitMessageLink(t, s, "https://115.com/s/new-share-code", options)
	if updated.Duplicate || !updated.ShareUpdate {
		t.Fatalf("different share code was not linked as update: %+v", updated)
	}
	var selection shareSelection
	if err := s.Store.GetSetting(context.Background(), "share_selection:"+updated.ID, &selection); err != nil {
		t.Fatal(err)
	}
	if len(selection.Selected) != 1 || selection.Selected[0].Relative != shareEpisode(2).Relative {
		t.Fatalf("expected only new episode, selected=%+v", selection.Selected)
	}
	completeSubmitted(t, s, updated)
	if len(p.received) != 2 || p.received[0] != shareEpisode(1).Entry.ID || p.received[1] != shareEpisode(2).Entry.ID {
		t.Fatalf("received files were not incremental: %v", p.received)
	}
}

func TestShareUpdatesDifferentShareCodeSameContentIsDuplicate(t *testing.T) {
	s, _ := shareUpdatesFixture(t)
	first := submitMessageLink(t, s, "https://115.com/s/old-share-code", ShareSubmissionOptions{CheckUpdates: true})
	completeSubmitted(t, s, first)
	second := submitMessageLink(t, s, "https://115.com/s/another-share-code", ShareSubmissionOptions{CheckUpdates: true})
	if !second.Duplicate || second.ID != first.ID || !strings.Contains(second.SubmissionMessage, "无需重复发送") {
		t.Fatalf("same-content regenerated share was not deduplicated: %+v", second)
	}
}

func TestShareUpdatesDifferentTMDBDoesNotCrossLink(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	first := submitMessageLink(t, s, "https://115.com/s/old-share-code", ShareSubmissionOptions{CheckUpdates: true})
	completeSubmitted(t, s, first)
	p.snapshot.Files = append(p.snapshot.Files, shareEpisode(2))
	j, err := s.SubmitShareMessage(context.Background(), "user1", "https://115.com/s/other-share-code", "fx1234", "movie", 1062807, ShareSubmissionOptions{CheckUpdates: true})
	if err != nil {
		t.Fatal(err)
	}
	if j.Duplicate || j.ShareUpdate {
		t.Fatal("different TMDB share was cross-linked")
	}
}

func TestShareCheckIntervalSkipPolicyForceFailuresAndConcurrentSubmissions(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	options := ShareSubmissionOptions{CheckUpdates: true, CheckInterval: 10 * time.Minute}
	first := submitMessage(t, s, options)
	active := submitMessage(t, s, options)
	if !active.Duplicate || p.checks != 1 {
		t.Fatal("active share checked again")
	}
	completeSubmitted(t, s, first)
	p.snapshot.Files = append(p.snapshot.Files, shareEpisode(2))
	cached := submitMessage(t, s, options)
	if !cached.Duplicate || p.checks != 1 || !strings.Contains(cached.SubmissionMessage, "检查间隔") {
		t.Fatal("cooldown ignored")
	}
	skipped := submitMessage(t, s, ShareSubmissionOptions{})
	if !skipped.Duplicate || p.checks != 1 {
		t.Fatal("skip policy ignored")
	}
	p.checkError = true
	if _, err := s.SubmitShareMessage(context.Background(), "user1", "https://115.com/s/share-code", "fx1234", "tv", 229192, ShareSubmissionOptions{CheckUpdates: true, Force: true}); err == nil {
		t.Fatal("failed check reported saved")
	}
	jobs, _ := s.Store.ListJobs(context.Background(), 200)
	if len(jobs) != 1 {
		t.Fatal("failed check created task")
	}
	p.checkError = false
	var wg sync.WaitGroup
	results := make(chan *store.TransferJob, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := s.SubmitShareMessage(context.Background(), "user1", "https://115.com/s/share-code", "fx1234", "tv", 229192, ShareSubmissionOptions{CheckUpdates: true, Force: true})
			if err != nil {
				t.Error(err)
				return
			}
			results <- j
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	var updateID string
	for j := range results {
		if !j.Duplicate {
			created++
		}
		if updateID != "" && j.ID != updateID {
			t.Fatal("concurrent revisions diverged")
		}
		updateID = j.ID
	}
	if created != 1 {
		t.Fatal("duplicate update jobs", created)
	}
}

func TestShareReceiveUncertainResultResumesWithoutSecondReceive(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	first := submitMessage(t, s, ShareSubmissionOptions{CheckUpdates: true})
	p.interrupted = true
	if err := s.process(context.Background(), first.ID); err == nil {
		t.Fatal("receive interruption ignored")
	}
	if len(p.received) != 1 {
		t.Fatal("receive not attempted")
	}
	steps, err := s.Store.Executions(context.Background())
	if err != nil || len(steps) != 1 {
		t.Fatal("missing receive checkpoint")
	}
	if err := s.RetryExecution(context.Background(), steps[0].ID); err != nil {
		t.Fatal("checkpoint retry did not queue job", err)
	}
	p.interrupted = false
	completeSubmitted(t, s, first)
	if len(p.received) != 1 {
		t.Fatal("uncertain receive repeated")
	}
}

func TestLegacyShareBaselineUsesReceivedOriginals(t *testing.T) {
	s, p := shareUpdatesFixture(t)
	ctx := context.Background()
	old, err := s.Store.CreateShareJob(ctx, &store.TransferJob{ID: "legacy", Source: "web", ShareURL: "https://115.com/s/share-code?password=fx1234", Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	old.StageCID = "old-stage"
	if err := s.Store.UpdateJob(ctx, old); err != nil {
		t.Fatal(err)
	}
	one := shareEpisode(1).Entry
	one.ID = "received-old-one"
	p.children["old-stage"] = []pan115.Entry{{ID: "old-work", Name: "Show", Directory: true}}
	p.children["old-work"] = []pan115.Entry{{ID: "old-season", Name: "Season 01", Directory: true}}
	p.children["old-season"] = []pan115.Entry{one}
	p.snapshot.Files = append(p.snapshot.Files, shareEpisode(2))
	update, err := s.SubmitShareMessage(ctx, "user1", "https://115.com/s/share-code", "", "tv", 229192, ShareSubmissionOptions{CheckUpdates: true})
	if err != nil {
		t.Fatal(err)
	}
	var selection shareSelection
	if err := s.Store.GetSetting(ctx, "share_selection:"+update.ID, &selection); err != nil {
		t.Fatal(err)
	}
	if update.Duplicate || len(selection.Selected) != 1 || selection.Selected[0].Entry.ID != shareEpisode(2).Entry.ID {
		t.Fatal("legacy baseline initialized from current updated share")
	}
	completeSubmitted(t, s, update)
	if len(p.received) != 1 || p.received[0] != shareEpisode(2).Entry.ID {
		t.Fatal("legacy original received again")
	}
}

func TestChangedShareContentSamePathAndStableOrder(t *testing.T) {
	old := []pan115.ShareFile{shareEpisode(1), shareEpisode(2)}
	current := []pan115.ShareFile{old[1], old[0]}
	if shareFingerprint(old) != shareFingerprint(current) || len(changedShareFiles(old, current)) != 0 {
		t.Fatal("listing order created update")
	}
	current[1].Entry.SHA1 = "new-sha"
	if changed := changedShareFiles(old, current); len(changed) != 1 || changed[0].Relative != old[0].Relative {
		t.Fatal("changed same-path content missed")
	}
	current[1] = old[0]
	current[1].Entry.ID = "another-id"
	if len(changedShareFiles(old, current)) != 0 {
		t.Fatal("same SHA1 recopy caused by remote ID change")
	}
}
