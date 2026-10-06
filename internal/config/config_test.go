package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSTRMPathCanUseExternalMount(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	strm := filepath.Join(root, "mounted-strm")
	t.Setenv("DATA_DIR", data)
	t.Setenv("STRM_PATH", strm)

	cfg := Load()
	if cfg.STRMPath() != strm {
		t.Fatalf("STRM path: got %q, want %q", cfg.STRMPath(), strm)
	}
	if err := cfg.Prepare(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{data, strm} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %q was not prepared: %v", path, err)
		}
	}
}
