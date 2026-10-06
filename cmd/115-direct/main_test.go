package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

func TestRuntimeLoggingDoesNotReenterStandardLogger(t *testing.T) {
	if os.Getenv("DIRECT_LOGGING_HELPER") == "1" {
		root := t.TempDir()
		vault, err := secure.LoadOrCreate(filepath.Join(root, "key"))
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(root, "db"), vault)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		configureLogging(st)
		slog.Info("delivery completed", "status", 302, "token", "TEST_SECRET")
		log.Print("standard logger completed")
		items, count, err := st.QueryLogs(context.Background(), store.LogFilter{Category: "runtime"})
		if err != nil || count != 2 || len(items) != 2 {
			t.Fatalf("duplicate or missing log records: %d %v", count, err)
		}
		for _, item := range items {
			if strings.Contains(item.Message, "TEST_SECRET") {
				t.Fatal("secret leaked in stored logs")
			}
		}
		return
	}
	// A subprocess isolates SetDefault's changes to the package-wide standard logger.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeLoggingDoesNotReenterStandardLogger$")
	cmd.Env = append(os.Environ(), "DIRECT_LOGGING_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime logging blocked or failed: %v\n%s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "delivery completed") || !strings.Contains(text, "standard logger completed") || strings.Contains(text, "TEST_SECRET") {
		t.Fatalf("invalid console logging: %s", text)
	}
}
