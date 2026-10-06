package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/local/115-direct/internal/store"
)

func TestReorganizeOneSuccessfulFileKeepsIDAndRefreshesAfterMetadata(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	o := DefaultOptions()
	o.Mode = "hardlink"
	if err := s.Store.PutSetting(ctx, "organization", o); err != nil {
		t.Fatal(err)
	}
	seedModern(t, s, c, "one", "The.Matrix.1999.1080p.mkv")
	seedModern(t, s, c, "other", "Unrelated.mkv")
	p := applyModern(t, s, c, "one", "hardlink", "first")
	before, _ := os.Stat(p.Items[0].Link.OutputPath)
	refreshes := 0
	s.Refresh = func(context.Context) error {
		refreshes++
		if _, err := os.Stat(filepath.Join(filepath.Dir(p.Items[0].Link.OutputPath), "movie.nfo")); err != nil {
			t.Fatal("refresh preceded metadata", err)
		}
		return nil
	}
	j, err := s.ReorganizeRecord(ctx, "organize:first:one")
	if err != nil {
		t.Fatal(err)
	}
	var plan OrganizePlan
	_ = json.Unmarshal(j.Actual, &plan)
	if len(plan.Items) != 1 || plan.Items[0].Media.RemoteID != "one" {
		t.Fatal("retry included unrelated files", plan.Items)
	}
	if err := s.process(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Store.GetMediaByRemoteID(ctx, "one")
	after, _ := os.Stat(m.STRMPath)
	if m.ID != "play-one" || !os.SameFile(before, after) || refreshes != 1 {
		t.Fatal("identity/link changed or refresh missing")
	}
	rows, n, err := s.Store.OrganizationRecords(ctx, store.RecordFilter{Search: j.ID})
	if err != nil || n != 1 || rows[0].Status != "success" {
		t.Fatalf("%v %d %v", rows, n, err)
	}
	l, _ := s.Store.MediaLink(ctx, "one")
	l.Deleted = "chain"
	_ = s.Store.PutLink(ctx, *l)
	if _, err := s.ReorganizeRecord(ctx, "organize:first:one"); err == nil {
		t.Fatal("deleted file regenerated")
	}
}

func TestReorganizeRefreshFailureDoesNotFailOrganization(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "The.Matrix.1999.1080p.mkv")
	applyModern(t, s, c, "one", "copy", "first")
	s.Refresh = func(context.Context) error { return fmt.Errorf("fixture offline") }
	j, err := s.ReorganizeRecord(ctx, "organize:first:one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process(ctx, j.ID); err != nil {
		t.Fatal("organization should succeed when Jellyfin notification is unavailable:", err)
	}
	rows, n, err := s.Store.OrganizationRecords(ctx, store.RecordFilter{Search: j.ID})
	if err != nil || n != 1 || rows[0].Status != "success" {
		t.Fatalf("%v %d %v", rows, n, err)
	}
	logs, _, err := s.Store.QueryLogs(ctx, store.LogFilter{Category: "jellyfin", Level: "warning", Search: "fixture offline"})
	if err != nil || len(logs) == 0 {
		t.Fatalf("Jellyfin notification failure was not retained as a warning: logs=%v err=%v", logs, err)
	}
}

func TestRetryCompletedCloudJobReconcilesExistingProjection(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "The.Matrix.1999.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	job := &store.TransferJob{ID: "cloud-completed", Source: "web", Status: "completed", StageCID: "original", Title: "The Matrix"}
	if err := s.Store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.UpdateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	s.Refresh = func(context.Context) error { refreshes++; return nil }
	if err := s.Retry(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.process(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Store.GetJob(ctx, job.ID)
	if err != nil || stored.Status != "completed" {
		t.Fatalf("job was not reconciled: %+v %v", stored, err)
	}
	if refreshes != 1 {
		t.Fatalf("expected one independent refresh, got %d", refreshes)
	}
	rows, total, err := s.Store.OrganizationRecords(ctx, store.RecordFilter{Search: job.ID})
	if err != nil || total != 1 || rows[0].OutputPath != p.Items[0].Link.OutputPath {
		t.Fatalf("projection missing after retry: rows=%+v total=%d err=%v", rows, total, err)
	}
}

func TestRetryPublishedOrganizationRecoversMappingAndRejectsMissingOutput(t *testing.T) {
	s, c, _ := modernFixture(t)
	ctx := context.Background()
	seedModern(t, s, c, "one", "The.Matrix.1999.1080p.mkv")
	p := applyModern(t, s, c, "one", "copy", "first")
	e, err := s.Store.Execution(ctx, "organize:first:one")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate interruption immediately after publishing but before mapping.
	e.Status = "published"
	if err := s.Store.PutExecution(ctx, *e); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.PutLink(ctx, *p.Items[0].Previous); err != nil {
		t.Fatal(err)
	}
	notified := 0
	s.Refresh = func(context.Context) error { notified++; return nil }
	if err := s.RetryExecution(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	l, _ := s.Store.MediaLink(ctx, "one")
	if l.OutputPath != p.Items[0].Link.OutputPath || notified != 1 {
		t.Fatal("mapping or notification not recovered")
	}
	if err := os.Remove(l.OutputPath); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryExecution(ctx, e.ID); err == nil {
		t.Fatal("missing output treated as completed")
	}
	if notified != 1 {
		t.Fatal("refresh sent for missing output")
	}
}
