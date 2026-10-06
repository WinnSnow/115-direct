package pan115

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SheltonZhu/115driver/pkg/driver"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func jsonResponse(body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestParseShareLink(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		explicit string
		code     string
		password string
	}{
		{name: "password query", raw: "https://115cdn.com/s/fixture-share-code?password=fx1234", code: "fixture-share-code", password: "fx1234"},
		{name: "receive code query", raw: "https://115.com/s/abc123?receive_code=a1b2", code: "abc123", password: "a1b2"},
		{name: "pwd query in message", raw: "分享 https://anxia.com/s/abc_123?pwd=z9y8 请查收", code: "abc_123", password: "z9y8"},
		{name: "explicit wins", raw: "https://115cdn.com/s/abc123?password=from-url", explicit: " manual ", code: "abc123", password: "manual"},
		{name: "no password", raw: "https://115cdn.com/s/abc123", code: "abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, password, err := parseShareLink(tt.raw, tt.explicit)
			if err != nil {
				t.Fatal(err)
			}
			if code != tt.code || password != tt.password {
				t.Fatalf("got code=%q password=%q, want code=%q password=%q", code, password, tt.code, tt.password)
			}
		})
	}
}

func TestAccountMapsUserAndSpaceAndCaches(t *testing.T) {
	requests := 0
	client := driver.New(driver.WithClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch req.URL.Scheme + "://" + req.URL.Host + req.URL.Path {
		case "https://my.115.com/":
			return jsonResponse(`{"state":true,"data":{"user_id":123,"user_name":"tester","vip":1,"expire":1770000000}}`), nil
		case "https://webapi.115.com/files/index_info":
			return jsonResponse(`{"state":true,"data":{"space_info":{"all_total":{"size":"1000","size_format":"1000 B"},"all_remain":{"size":"250","size_format":"250 B"},"all_use":{"size":"750","size_format":"750 B"}},"login_devices_info":{"list":[]}}}`), nil
		default:
			t.Fatalf("unexpected account request: %s", req.URL.String())
			return nil, nil
		}
	})}))
	provider := New()
	provider.client = client
	provider.cookie = "UID=test"

	account, err := provider.Account(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if account.UserID != 123 || account.Username != "tester" || !account.VIP || account.VIPLevel != 1 || account.VIPExpire != 1770000000 {
		t.Fatalf("unexpected user mapping: %#v", account)
	}
	if account.SpaceTotal != 1000 || account.SpaceUsed != 750 || account.SpaceRemain != 250 ||
		account.SpaceTotalText != "1000 B" || account.SpaceUsedText != "750 B" || account.SpaceRemainText != "250 B" {
		t.Fatalf("unexpected space mapping: %#v", account)
	}
	if requests != 2 {
		t.Fatalf("requests after first account call = %d, want 2", requests)
	}

	cached, err := provider.Account(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if cached == account {
		t.Fatal("cached account should be returned as a copy")
	}
	if requests != 2 {
		t.Fatalf("cached call made requests: got %d, want 2", requests)
	}

	if _, err := provider.Account(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if requests != 4 {
		t.Fatalf("forced refresh requests = %d, want 4", requests)
	}
}

func TestParseShareLinkRejectsInvalidURL(t *testing.T) {
	if _, _, err := parseShareLink("https://example.com/s/abc", ""); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestSnapshotShareUsesCDNEndpointAndURLPassword(t *testing.T) {
	provider := New()
	provider.cookie = "UID=test"
	provider.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "115cdn.com" || req.URL.Path != "/webapi/share/snap" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		if got := req.Header.Get("User-Agent"); got != driver.UADefault {
			t.Fatalf("User-Agent=%q", got)
		}
		if got := req.URL.Query().Get("receive_code"); got != "fx1234" {
			t.Fatalf("receive_code=%q", got)
		}
		if got := req.Header.Get("Referer"); got != "https://115cdn.com/s/fixture-share-code?password=fx1234&" {
			t.Fatalf("Referer=%q", got)
		}
		return jsonResponse(`{"state":true,"data":{"list":[{"fid":"f1","n":"video.mkv","sha":"sha1","pc":"pick","s":"123","fc":1}],"shareinfo":{"share_title":"Title"}}}`), nil
	})}

	snapshot, err := provider.SnapshotShare(context.Background(), "https://115cdn.com/s/fixture-share-code?password=fx1234", "")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Code != "fixture-share-code" || snapshot.ReceiveCode != "fx1234" || snapshot.Title != "Title" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Size != 123 || snapshot.Entries[0].SHA1 != "sha1" {
		t.Fatalf("unexpected entries: %#v", snapshot.Entries)
	}
}

func TestReceiveShareUsesSnapshotPassword(t *testing.T) {
	provider := New()
	provider.cookie = "UID=test"
	provider.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != shareReceiveEndpoint {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("share_code") != "share123" || form.Get("receive_code") != "fx1234" || form.Get("file_id") != "f1,f2" || form.Get("cid") != "target" {
			t.Fatalf("unexpected form: %v", form)
		}
		if got := req.Header.Get("Referer"); got != "https://115cdn.com/s/share123?password=fx1234&" {
			t.Fatalf("Referer=%q", got)
		}
		return jsonResponse(`{"state":true}`), nil
	})}

	snapshot := &ShareSnapshot{Code: "share123", ReceiveCode: "fx1234", Entries: []Entry{{ID: "f1"}, {ID: "f2"}}}
	if err := provider.ReceiveShare(context.Background(), snapshot, "", "target"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalQRStatusIsIdempotent(t *testing.T) {
	provider := New()
	provider.qrMu.Lock()
	first := provider.finishQRLocked("session", QRStatus{State: "confirmed", Cookie: "UID=test"})
	provider.qrMu.Unlock()
	if first.State != "confirmed" {
		t.Fatalf("unexpected first status: %#v", first)
	}

	second, err := provider.PollQR(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if second.State != "confirmed" || second.Cookie != "UID=test" {
		t.Fatalf("unexpected repeated status: %#v", second)
	}
}

func TestListPrefersAndroidEndpoint(t *testing.T) {
	provider := New()
	provider.cookie = "UID=test"
	provider.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == "" || req.URL.Scheme+"://"+req.URL.Host+req.URL.Path != androidFileListEndpoint {
			t.Fatalf("unexpected list endpoint: %s", req.URL.String())
		}
		if got := req.URL.Query().Get("cid"); got != "parent" {
			t.Fatalf("cid=%q", got)
		}
		return jsonResponse(`{"state":true,"cid":"parent","count":2,"offset":0,"data":[{"fid":"child","pid":"parent","fn":"电影","fc":"0","fs":"0","pc":"dir-pick","uppt":"100","uet":"200"},{"fid":"file","pid":"parent","fn":"video.mkv","fc":"1","fs":"123","sha1":"abc","pc":"file-pick","uppt":"300","uet":"400"}]}`), nil
	})}

	entries, err := provider.List(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "child" || entries[0].Name != "电影" || !entries[0].Directory ||
		entries[1].ID != "file" || entries[1].Directory || entries[1].Size != 123 || entries[1].SHA1 != "abc" || entries[1].PickCode != "file-pick" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
}

func TestTerminalQRStatusExpires(t *testing.T) {
	provider := New()
	provider.qrDone["session"] = terminalQRStatus{
		status:  QRStatus{State: "confirmed"},
		expires: time.Now().Add(-time.Second),
	}
	if _, err := provider.PollQR(context.Background(), "session"); err == nil {
		t.Fatal("expired QR status should not be found")
	}
}
