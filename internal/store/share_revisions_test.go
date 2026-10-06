package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestShareRevisionAtomicReservationFrozenSelectionAndMigration(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	first, err := st.CreateShareRevision(ctx, &TransferJob{ID: "first", Source: "wecom", ShareURL: "https://115.com/s/same", Status: "queued"}, "revision-one", "", map[string]string{"password": "sensitive-selection"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.GetSetting(ctx, "share_selection:first", &map[string]string{}); err != nil {
		t.Fatal("selection not persisted atomically")
	}
	var raw string
	if err := st.db.QueryRow(`SELECT value FROM settings WHERE key='share_selection:first'`).Scan(&raw); err != nil || strings.Contains(raw, "sensitive-selection") {
		t.Fatal("selection not encrypted")
	}
	blocked, err := st.CreateShareRevision(ctx, &TransferJob{ID: "blocked", Source: "wecom", ShareURL: "https://115cdn.com/s/same", Status: "queued"}, "revision-two", first.ID, map[string]string{})
	if err != nil || !blocked.Duplicate || blocked.ID != first.ID {
		t.Fatal("active revision allowed new transfer")
	}
	first.Status = "completed"
	if err := st.UpdateJob(ctx, first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *TransferJob, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, err := st.CreateShareRevision(ctx, &TransferJob{ID: fmt.Sprint("update-", i), Source: "wecom", ShareURL: "https://anxia.com/s/same", Status: "queued"}, "revision-two", first.ID, map[string]string{"relative": "new-episode"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- j
		}(i)
	}
	wg.Wait()
	close(results)
	created := 0
	latest := ""
	for j := range results {
		if !j.Duplicate {
			created++
		}
		if latest != "" && latest != j.ID {
			t.Fatal("revision identity diverged")
		}
		latest = j.ID
	}
	if created != 1 {
		t.Fatal("created duplicate revisions", created)
	}
	if err := st.migrateShareReceipts(); err != nil {
		t.Fatal(err)
	}
	receipt, err := st.ShareJob(ctx, "https://115.com/s/same")
	if err != nil || receipt.ID != latest {
		t.Fatal("migration reset latest receipt")
	}
	jobs, _, err := st.TransferRecords(ctx, RecordFilter{})
	if err != nil || len(jobs) != 2 {
		t.Fatal("revision history missing")
	}
}
