package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

type browsingPan struct {
	pan115.Provider
	requested []string
}

func (p *browsingPan) List(_ context.Context, cid string) ([]pan115.Entry, error) {
	p.requested = append(p.requested, cid)
	return []pan115.Entry{{ID: "video", Name: "video.mkv"}, {ID: "folder", Name: "folder", Directory: true}}, nil
}

func TestCloudBrowsingSupportsRootWithoutConfiguredLibrary(t *testing.T) {
	pan := &browsingPan{}
	s := &Server{Pan: pan}
	for _, test := range []struct{ query, cid string }{
		{"", "0"}, {"?cid=0", "0"}, {"?cid=inbox", "inbox"}, {"?cid=other", "other"},
	} {
		w := httptest.NewRecorder()
		s.browse115(w, httptest.NewRequest(http.MethodGet, "/api/v1/files/115"+test.query, nil))
		var result struct {
			CID     string         `json:"cid"`
			Entries []pan115.Entry `json:"entries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || result.CID != test.cid || pan.requested[len(pan.requested)-1] != test.cid || len(result.Entries) != 2 || !result.Entries[0].Directory {
			t.Fatalf("%s: status=%d result=%+v", test.query, w.Code, result)
		}
	}
}

func newFileTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	vault, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"), vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSecureLocalPathStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "电视剧")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "episode.strm")
	if err := os.WriteFile(file, []byte("http://direct/media"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, rel, err := secureLocalPath(root, "电视剧/episode.strm")
	if err != nil || got != file || rel != filepath.Join("电视剧", "episode.strm") {
		t.Fatalf("path=%q rel=%q err=%v", got, rel, err)
	}
	for _, invalid := range []string{"../etc/passwd", "/etc/passwd"} {
		if _, _, err := secureLocalPath(root, invalid); err == nil {
			t.Fatalf("accepted invalid path %q", invalid)
		}
	}
}

func TestSecureLocalPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := secureLocalPath(root, "outside"); err == nil {
		t.Fatal("accepted symlink outside root")
	}
}

func TestSTRMBrowseAndContentHandlers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "电视剧")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "S01E01.strm"), []byte("http://direct:9096/media/token"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("not media"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: newFileTestStore(t), STRMRoot: root}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/strm?path="+"%E7%94%B5%E8%A7%86%E5%89%A7", nil)
	w := httptest.NewRecorder()
	server.browseSTRM(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("browse status=%d body=%s", w.Code, w.Body.String())
	}
	var listing struct {
		Path    string       `json:"path"`
		Entries []localEntry `json:"entries"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if listing.Path != "电视剧" || len(listing.Entries) != 2 {
		t.Fatalf("unexpected listing: %#v", listing)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/strm/content?path="+"%E7%94%B5%E8%A7%86%E5%89%A7%2FS01E01.strm", nil)
	w = httptest.NewRecorder()
	server.readSTRM(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("content status=%d body=%s", w.Code, w.Body.String())
	}
	var content struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(w.Body).Decode(&content); err != nil {
		t.Fatal(err)
	}
	if content.Content != "http://direct:9096/media/token" {
		t.Fatalf("unexpected content: %q", content.Content)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/strm/content?path="+"%E7%94%B5%E8%A7%86%E5%89%A7%2Fnote.txt", nil)
	w = httptest.NewRecorder()
	server.readSTRM(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-strm status=%d body=%s", w.Code, w.Body.String())
	}
}
