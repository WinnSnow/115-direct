package syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type blockingPan struct {
	*treePan
	started, release chan struct{}
}

func (p *blockingPan) List(ctx context.Context, cid string) ([]pan115.Entry, error) {
	close(p.started)
	select {
	case <-p.release:
		return nil, fmt.Errorf("fixture offline")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestQueueExcludesConcurrentScanAndPersistsFailure(t *testing.T) {
	st, ctx := syncStore(t), context.Background()
	p := &blockingPan{treePan: &treePan{}, started: make(chan struct{}), release: make(chan struct{})}
	s := New(st, p, nil, organize.DirectoryConfig{LibraryCID: "library", STRMPath: t.TempDir()})
	if err := s.Queue(true); err != nil {
		t.Fatal(err)
	}
	<-p.started
	if err := s.Queue(false); err == nil {
		t.Fatal("overlapping scan accepted")
	}
	status := s.Status()
	if !status.Running || status.Mode != "full" || status.RecordID == "" {
		t.Fatalf("%+v", status)
	}
	items, total, err := st.SyncRecords(ctx, store.RecordFilter{})
	if err != nil || total != 1 || items[0].Status != "running" {
		t.Fatalf("%v %d %v", items, total, err)
	}
	close(p.release)
	deadline := time.Now().Add(time.Second * 5)
	for s.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond * 10)
	}
	items, total, err = st.SyncRecords(ctx, store.RecordFilter{Status: "failed"})
	if err != nil || total != 1 || items[0].Error == "" || items[0].FinishedAt == nil || s.Status().Running {
		t.Fatalf("%v %d %v", items, total, err)
	}
}

func TestSyncRecordSuccessfulCounters(t *testing.T) {
	st, ctx := syncStore(t), context.Background()
	p := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "file", Name: "Film.mkv", PickCode: "pick"}}}}
	s := New(st, p, nil, organize.DirectoryConfig{LibraryCID: "library", STRMPath: t.TempDir(), GatewayURL: "https://gateway.example"})
	if err := s.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFull(ctx); err != nil {
		t.Fatal(err)
	}
	items, total, err := st.SyncRecords(ctx, store.RecordFilter{})
	if err != nil || total != 2 || items[0].Mode != "full" || items[1].Mode != "incremental" || items[1].Created != 1 || items[0].Created != 0 || items[0].Files != 1 {
		t.Fatalf("%v %d %v", items, total, err)
	}
}
