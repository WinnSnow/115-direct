package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/secure"
)

func TestLogRedactionPaginationAndRetention(t *testing.T) {
	root := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(root, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	message := Redact("Authorization: Bearer TOPSECRET\nCookie: UID=SECRET; password=PASSWORD; https://cdn.example/file?sig=SIGNED https://user:PASS@proxy.example")
	for _, secret := range []string{"TOPSECRET", "SECRET", "PASSWORD", "SIGNED", "PASS@"} {
		if strings.Contains(message, secret) {
			t.Errorf("secret leaked: %s", message)
		}
	}
	c := DefaultLogConfig()
	c.MaxEntries = 100
	_ = st.PutSetting(ctx, "logging", c)
	for i := 0; i < 110; i++ {
		st.Log(ctx, "organize", "warning", "file operation")
	}
	items, total, err := st.QueryLogs(ctx, LogFilter{Category: "organize", Level: "warning", Limit: 10, Offset: 10})
	if err != nil || len(items) != 10 || total != 100 {
		t.Fatalf("%d %d %v", len(items), total, err)
	}
	st.Log(ctx, "runtime", "debug", "debug hidden")
	items, total, err = st.QueryLogs(ctx, LogFilter{Category: "runtime"})
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatal("log level ignored")
	}
}

func TestRecoveredReviewReopensButIgnoredMissingDoesNot(t *testing.T) {
	root := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(root, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(root, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.QueueReview(ctx, "local:one", "one", "missing")
	_ = st.ResolveReview(ctx, "local:one", "ignored")
	_ = st.QueueReview(ctx, "local:one", "one", "still missing")
	r, _ := st.Reviews(ctx)
	if r[0].Status != "ignored" {
		t.Fatal("ignored review reopened")
	}
	_ = st.ResolveReview(ctx, "local:one", "resolved")
	_ = st.QueueReview(ctx, "local:one", "one", "missing again")
	r, _ = st.Reviews(ctx)
	if r[0].Status != "pending" {
		t.Fatal("recovered review not reopened")
	}
}
