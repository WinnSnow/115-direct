package syncer

import (
	"context"
	"fmt"
	"testing"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
)

func TestSyncRefreshAfterLocalScanAndQueueEvenWhenUnchanged(t *testing.T) {
	st, ctx := syncStore(t), context.Background()
	p := &treePan{children: map[string][]pan115.Entry{"library": {{ID: "file", Name: "Film.mkv", PickCode: "pick"}}}}
	s := New(st, p, nil, organize.DirectoryConfig{LibraryCID: "library", STRMPath: t.TempDir(), GatewayURL: "https://gateway.example"})
	queued, notified := 0, 0
	s.OnChanged = func(context.Context) error { queued++; return nil }
	s.Refresh = func(context.Context) error {
		notified++
		if queued != 1 {
			t.Fatal("refresh before organization queue")
		}
		items, err := st.ListMedia(ctx)
		if err != nil || len(items) != 1 {
			t.Fatal("refresh before local mapping")
		}
		return nil
	}
	if err := s.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RunIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if notified != 2 {
		t.Fatalf("unchanged scan omitted refresh: %d", notified)
	}
	s.Refresh = func(context.Context) error { return fmt.Errorf("fixture offline") }
	if err := s.RunFull(ctx); err == nil {
		t.Fatal("notification failure lost")
	}
}
