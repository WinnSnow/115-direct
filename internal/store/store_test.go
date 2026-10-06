package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/secure"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	vault, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(dir, "test.db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestEncryptedSettingsJobsAndMedia(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	value := map[string]string{"token": "very-secret"}
	if err := st.PutSetting(ctx, "tmdb", value); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.db.QueryRow(`SELECT value FROM settings WHERE key='tmdb'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "very-secret") {
		t.Fatal("setting persisted in plaintext")
	}
	var got map[string]string
	if err := st.GetSetting(ctx, "tmdb", &got); err != nil || got["token"] != "very-secret" {
		t.Fatalf("setting: %#v %v", got, err)
	}
	job := &TransferJob{ID: "job", Source: "test", ShareURL: "https://115.com/s/x", Status: "queued"}
	if err := st.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetJob(ctx, "job")
	if err != nil || loaded.Status != "queued" {
		t.Fatalf("job: %#v %v", loaded, err)
	}
	media := MediaEntry{ID: "media", RemoteID: "remote", PickCode: "pc", Name: "movie.mkv", STRMPath: "/tmp/movie.strm"}
	if err := st.PutMedia(ctx, media); err != nil {
		t.Fatal(err)
	}
	if found, err := st.GetMediaByRemoteID(ctx, "remote"); err != nil || found.ID != "media" {
		t.Fatalf("media: %#v %v", found, err)
	}
}

func TestMessageDeduplication(t *testing.T) {
	st := testStore(t)
	first, err := st.DeduplicateMessage(context.Background(), "key")
	if err != nil || !first {
		t.Fatal("first insert failed")
	}
	second, err := st.DeduplicateMessage(context.Background(), "key")
	if err != nil || second {
		t.Fatal("duplicate was accepted")
	}
}

func TestAuditListingFiltersNewestFirst(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	st.Audit(ctx, "sync", "first")
	st.Audit(ctx, "auth", "second")
	st.Audit(ctx, "sync", "third")

	events, err := st.ListAudit(ctx, "sync", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Message != "third" || events[1].Message != "first" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestUploadTaskPersistsAndDeduplicatesByFingerprint(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	task := &UploadTask{ID: "upload-1", LocalPath: "/data/a.mkv", TargetCID: "42", Filename: "a.mkv", Size: 10, SHA1: "ABC", Status: "queued", Channel: "auto"}
	if err := st.CreateUploadTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	found, err := st.FindUploadTask(ctx, task.LocalPath, task.TargetCID, task.SHA1, task.Size)
	if err != nil || found.ID != task.ID {
		t.Fatalf("find upload: %#v %v", found, err)
	}
	task.Status, task.RemoteID, task.Rapid = "completed", "remote-1", true
	if err := st.UpdateUploadTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetUploadTask(ctx, task.ID)
	if err != nil || loaded.Status != "completed" || loaded.RemoteID != "remote-1" || !loaded.Rapid {
		t.Fatalf("loaded upload: %#v %v", loaded, err)
	}
}
