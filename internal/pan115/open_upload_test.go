package pan115

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestOpenUploaderRapidUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/upload/init" || r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("unexpected request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":true,"data":{"status":2,"file_id":"remote-1","pick_code":"pick-1"}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("movie"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (&OpenUploader{HTTP: server.Client(), BaseURL: server.URL, Creds: OpenCredentials{AccessToken: "access"}}).Upload(context.Background(), path, "42", "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteID != "remote-1" || result.PickCode != "pick-1" || !result.Rapid {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestOpenUploaderSecondInitUsesBaseURL(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/upload/init" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"state":true,"data":{"status":6,"file_id":"remote-1","sign_key":"key","sign_check":"0-0"}}`)
			return
		}
		fmt.Fprint(w, `{"state":true,"data":{"status":2,"file_id":"remote-1","pick_code":"pick-1"}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("movie"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (&OpenUploader{HTTP: server.Client(), BaseURL: server.URL, Creds: OpenCredentials{AccessToken: "access"}}).Upload(context.Background(), path, "42", "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || result.RemoteID != "remote-1" || !result.Rapid {
		t.Fatalf("unexpected result calls=%d result=%#v", calls.Load(), result)
	}
}
