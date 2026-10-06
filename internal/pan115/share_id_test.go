package pan115

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestShareIDDecoding(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{`"9000000000000000001"`, "9000000000000000001"},
		{`9000000000000000001`, "9000000000000000001"},
		{`18446744073709551615`, "18446744073709551615"},
		{`"folder"`, "folder"},
		{`""`, ""},
		{`null`, ""},
		{` 0 `, "0"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			id := shareID("previous")
			if err := json.Unmarshal([]byte(tt.input), &id); err != nil || string(id) != tt.want {
				t.Fatalf("decoded ID %q, error %v; want %q", id, err, tt.want)
			}
		})
	}
	for _, input := range []string{`true`, `{}`, `[]`, `-1`, `1.5`, `1e3`} {
		t.Run(input, func(t *testing.T) {
			var id shareID
			if err := json.Unmarshal([]byte(input), &id); err == nil {
				t.Fatal("invalid ID accepted")
			}
		})
	}
}

func TestShareTreeMixedIDsPreservesReceiveIDs(t *testing.T) {
	p := New()
	p.cookie = "UID=fixture"
	received := false
	p.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			if r.URL.String() != shareReceiveEndpoint {
				t.Fatalf("unexpected receive endpoint: %s", r.URL.Path)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("file_id") != "9000000000000000001,9000000000000000002" ||
				r.Form.Get("receive_code") != "fx1234" || r.Form.Get("cid") != "target" {
				t.Fatal("IDs or password changed during receive")
			}
			received = true
			return jsonResponse(`{"state":true}`), nil
		}
		if r.URL.Query().Get("receive_code") != "fx1234" {
			t.Fatal("password lost in snapshot")
		}
		switch r.URL.Query().Get("cid") {
		case "0":
			return jsonResponse(`{"state":true,"data":{"list":[{"fid":null,"cid":9000000000000000001,"n":"Show","fc":0},{"fid":"9000000000000000002","cid":0,"n":"Movie.mkv","fc":1,"s":"20"}]}}`), nil
		case "9000000000000000001":
			return jsonResponse(`{"state":true,"data":{"list":[{"fid":9000000000000000003,"cid":"9000000000000000001","n":"Show.S01E01.mkv","fc":1,"s":"10"},{"fid":"9000000000000000004","cid":null,"n":"Show.S01E02.mkv","fc":1,"s":"10"}]}}`), nil
		default:
			t.Fatalf("directory ID changed: %s", r.URL.Query().Get("cid"))
			return nil, nil
		}
	})}
	snapshot, err := p.SnapshotShareTree(context.Background(), "https://115.com/s/mixed?password=fx1234", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || !snapshot.Entries[0].Directory || snapshot.Entries[1].Directory {
		t.Fatalf("wrong root file/directory mapping: %+v", snapshot.Entries)
	}
	wantIDs := []string{"9000000000000000003", "9000000000000000004", "9000000000000000002"}
	wantPaths := []string{"Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv", "Movie.mkv"}
	if len(snapshot.Files) != len(wantIDs) {
		t.Fatalf("wrong file count: %d", len(snapshot.Files))
	}
	for i, file := range snapshot.Files {
		if file.Entry.ID != wantIDs[i] || file.Relative != wantPaths[i] || file.Entry.Directory {
			t.Fatalf("wrong file mapping: %+v", file)
		}
	}
	if err := p.ReceiveShare(context.Background(), snapshot, "", "target"); err != nil {
		t.Fatal(err)
	}
	if !received {
		t.Fatal("receive request not sent")
	}
}
