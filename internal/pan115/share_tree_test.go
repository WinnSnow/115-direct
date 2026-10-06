package pan115

import (
	"context"
	"net/http"
	"testing"
)

func TestShareTreeFindsNestedEpisodeInsideUnchangedFolder(t *testing.T) {
	p := New()
	p.cookie = "UID=fixture"
	added := false
	p.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("receive_code") != "fx1234" {
			t.Fatal("password lost while browsing nested share")
		}
		switch r.URL.Query().Get("cid") {
		case "0":
			return jsonResponse(`{"state":true,"data":{"shareinfo":{"share_title":"Show"},"list":[{"cid":"work","n":"Show","fc":0}]}}`), nil
		case "work":
			return jsonResponse(`{"state":true,"data":{"list":[{"cid":"season","n":"Season 01","fc":0}]}}`), nil
		case "season":
			if added {
				return jsonResponse(`{"state":true,"data":{"list":[{"fid":"one","n":"Show.S01E01.mkv","fc":1,"sha":"sha-one","s":"10"},{"fid":"two","n":"Show.S01E02.mkv","fc":1,"sha":"sha-two","s":"20"}]}}`), nil
			}
			return jsonResponse(`{"state":true,"data":{"list":[{"fid":"one","n":"Show.S01E01.mkv","fc":1,"sha":"sha-one","s":"10"}]}}`), nil
		}
		t.Fatalf("unexpected share directory")
		return nil, nil
	})}
	first, err := p.SnapshotShareTree(context.Background(), "https://115.com/s/tree?password=fx1234", "")
	if err != nil || len(first.Files) != 1 || first.Files[0].Relative != "Show/Season 01/Show.S01E01.mkv" {
		t.Fatalf("first tree %+v %v", first, err)
	}
	added = true
	second, err := p.SnapshotShareTree(context.Background(), "https://115.com/s/tree?password=fx1234", "")
	if err != nil || len(second.Files) != 2 || first.Entries[0].ID != second.Entries[0].ID {
		t.Fatal("nested update not found", err)
	}
}

func TestShareTreeRejectsAmbiguousPaths(t *testing.T) {
	p := New()
	p.cookie = "UID=fixture"
	p.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"state":true,"data":{"list":[{"fid":"one","n":"Show.mkv","fc":1,"s":"10"},{"fid":"two","n":"Show.mkv","fc":1,"s":"20"}]}}`), nil
	})}
	if _, err := p.SnapshotShareTree(context.Background(), "https://115.com/s/tree", "fx1234"); err == nil {
		t.Fatal("ambiguous share paths accepted")
	}
}
