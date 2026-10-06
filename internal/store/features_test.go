package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestShareReservationConcurrentAndCanonical(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	urls := []string{"https://115.com/s/ABC123?password=a", "https://www.anxia.com/s/ABC123", "https://115cdn.com/s/ABC123?password=b"}
	var wg sync.WaitGroup
	results := make(chan *TransferJob, 18)
	for i := 0; i < 18; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, err := st.CreateShareJob(ctx, &TransferJob{ID: fmt.Sprint(i), Source: "web", ShareURL: urls[i%3], Status: "queued"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- j
		}(i)
	}
	wg.Wait()
	close(results)
	newJobs, id := 0, ""
	for j := range results {
		if !j.Duplicate {
			newJobs++
		}
		if id != "" && id != j.ID {
			t.Fatal("duplicate identity changed")
		}
		id = j.ID
	}
	if newJobs != 1 {
		t.Fatalf("created %d jobs", newJobs)
	}
	items, total, err := st.TransferRecords(ctx, RecordFilter{})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("%v %d %v", items, total, err)
	}
	if _, err := st.CreateShareJob(ctx, &TransferJob{ID: "invalid", ShareURL: "https://example.org/"}); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

func TestLegacyReceiptPrefersCompletedAndRecordsExcludeLocalJobs(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	for _, j := range []TransferJob{
		{ID: "failed", Source: "web", ShareURL: "https://115.com/s/legacy", Status: "failed"},
		{ID: "saved", Source: "wecom", ShareURL: "https://anxia.com/s/legacy", Status: "completed", Title: "Movie"},
		{ID: "local", Source: "manual", Status: "completed"},
	} {
		if err := st.CreateJob(ctx, &j); err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateJob(ctx, &j); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.migrateShareReceipts(); err != nil {
		t.Fatal(err)
	}
	j, err := st.CreateShareJob(ctx, &TransferJob{ID: "new", Source: "web", ShareURL: "https://115cdn.com/s/legacy"})
	if err != nil || !j.Duplicate || j.ID != "saved" {
		t.Fatalf("%+v %v", j, err)
	}
	items, total, err := st.TransferRecords(ctx, RecordFilter{Search: "Movie", Status: "completed", Limit: 1})
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != "saved" {
		t.Fatalf("%v %d %v", items, total, err)
	}
}

func TestPersistentSyncHistoryAndRecovery(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	r := SyncRecord{ID: "run", Mode: "full", Trigger: "manual", Status: "running", LibraryCID: "library", StartedAt: time.Now().UTC()}
	if err := st.PutSyncRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverSyncRecords(ctx); err != nil {
		t.Fatal(err)
	}
	items, total, err := st.SyncRecords(ctx, RecordFilter{Status: "interrupted"})
	if err != nil || total != 1 || items[0].FinishedAt == nil {
		t.Fatalf("%v %d %v", items, total, err)
	}
	r.Status, r.Files, r.Created = "completed", 5, 3
	if err := st.PutSyncRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	items, total, err = st.SyncRecords(ctx, RecordFilter{})
	if err != nil || total != 1 || items[0].Files != 5 || items[0].Created != 3 {
		t.Fatalf("%v %d %v", items, total, err)
	}
}

func TestRecognitionCacheLifetimeLimitsDisableAndDelete(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	config := DefaultCacheConfig()
	config.MaxEntries, config.MaxBytes = 10, 1<<20
	if err := st.PutSetting(ctx, "recognition", config); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := st.PutCached(ctx, fmt.Sprint(i), "match", "Movie", []byte(`{"id":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := st.CacheEntries(ctx, RecordFilter{})
	if err != nil || total != 10 || len(items) != 10 {
		t.Fatalf("%v %d %v", items, total, err)
	}
	key := items[0].Key
	if raw, hit, err := st.Cached(ctx, key); err != nil || !hit || string(raw) != `{"id":1}` {
		t.Fatalf("%s %v %v", raw, hit, err)
	}
	if _, err := st.db.Exec(`UPDATE recognition_cache SET expires_at=? WHERE key=?`, time.Now().Add(-time.Hour).UTC(), key); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := st.Cached(ctx, key); err != nil || hit {
		t.Fatalf("expired hit %v %v", hit, err)
	}
	config.Enabled = false
	if err := st.PutSetting(ctx, "recognition", config); err != nil {
		t.Fatal(err)
	}
	if _, hit, _ := st.Cached(ctx, items[1].Key); hit {
		t.Fatal("disabled cache used")
	}
	config.Enabled = true
	if err := st.PutSetting(ctx, "recognition", config); err != nil {
		t.Fatal(err)
	}
	large := []byte(`{"data":"` + strings.Repeat("x", 600000) + `"}`)
	for _, key := range []string{"large1", "large2"} {
		if err := st.PutCached(ctx, key, "details", key, large); err != nil {
			t.Fatal(err)
		}
	}
	var bytes int
	if err := st.db.QueryRow(`SELECT COALESCE(SUM(length(body)),0) FROM recognition_cache`).Scan(&bytes); err != nil || bytes > config.MaxBytes {
		t.Fatalf("%d %v", bytes, err)
	}
	if err := st.DeleteCached(ctx, "all"); err != nil {
		t.Fatal(err)
	}
	_, total, _ = st.CacheEntries(ctx, RecordFilter{})
	if total != 0 {
		t.Fatal("cache not cleared")
	}
	if err := st.PutCached(ctx, "bad", "match", "bad", []byte("not json")); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
