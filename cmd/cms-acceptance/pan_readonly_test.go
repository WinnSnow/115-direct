package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReuseOnlyTransportRejectsLoginAndMutationsBeforeNetwork(t *testing.T) {
	calls := 0
	guard := &reuseOnlyTransport{maximum: 20, base: testTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"state":true}`))}, nil
	})}
	for _, item := range []struct{ method, url string }{
		{"GET", "https://passportapi.115.com/app/1.0/web/1.0/check/sso"},
		{"POST", "https://passportapi.115.com/app/1.0/qandroid/1.0/login/qrcode/"},
		{"GET", "https://qrcodeapi.115.com/api/1.0/web/1.0/token/"},
		{"GET", "https://passportapi.115.com/app/1.0/web/1.0/logout/logout"},
		{"POST", "https://webapi.115.com/rb/delete"},
		{"POST", "https://webapi.115.com/files/move"},
		{"POST", "https://webapi.115.com/files/copy"},
		{"POST", "https://webapi.115.com/files/add"},
		{"GET", "https://webapi.115.com/files?cid=0"},
		{"POST", "https://uplb.115.com/4.0/initupload.php"},
		{"GET", "http://cms.example.test:9527/d/fixturepick123"},
		{"GET", "http://my.115.com/?ct=guide&ac=status"},
		{"GET", "https://my.115.com:443/?ct=guide&ac=status"},
		{"POST", "https://my.115.com/?ct=guide&ac=status"},
		{"GET", "https://my.115.com/?ct=ajax&ac=login"},
		{"GET", "https://my.115.com/?ct=guide&ac=status&ac=login"},
		{"GET", "https://my.115.com/?ct=guide&ac=status&login=1"},
	} {
		r, _ := http.NewRequest(item.method, item.url, nil)
		if _, err := guard.RoundTrip(r); err == nil {
			t.Fatalf("forbidden request accepted: %s", item.url)
		}
	}
	if calls != 0 {
		t.Fatal("forbidden operation reached network")
	}
	for _, item := range []struct{ method, url string }{
		{"GET", "https://my.115.com/?ct=guide&ac=status"},
		{"POST", "https://proapi.115.com/app/chrome/downurl?t=1"},
	} {
		r, _ := http.NewRequest(item.method, item.url, nil)
		resp, err := guard.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls != 2 {
		t.Fatal("approved requests not forwarded")
	}
	requests, blocked := guard.counts()
	if requests != 2 || blocked != 17 {
		t.Fatalf("unexpected audit counts: %d/%d", requests, blocked)
	}
}

func TestScopeRequiresUniqueFileAndVerifiedAncestry(t *testing.T) {
	valid := `{"roots":{"转存文件夹":"100","媒体库":"200"},"samples":[{"pickcode":"samplepick123","matches":[{"file_id":"300","pickcode":"samplepick123","scope_id":"200","ancestry_verified":true,"ancestry":[{"file_id":0},{"file_id":"200"}]}]}]}`
	picks, err := verifiedPicks([]byte(valid))
	if err != nil || len(picks) != 1 {
		t.Fatalf("valid scope rejected: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"ancestry_verified":true`, `"ancestry_verified":false`, 1),
		strings.Replace(valid, `"scope_id":"200"`, `"scope_id":"999"`, 1),
		strings.Replace(valid, `{"file_id":"200"}`, `{"file_id":"999"}`, 1),
		strings.Replace(valid, `"媒体库":"200"`, `"媒体库":"0"`, 1),
	} {
		if _, err := verifiedPicks([]byte(raw)); err == nil {
			t.Fatal("unverified scope accepted")
		}
	}
	var scope sampleScope
	json.Unmarshal([]byte(valid), &scope)
	scope.Samples[0].Matches = append(scope.Samples[0].Matches, scope.Samples[0].Matches[0])
	raw, _ := json.Marshal(scope)
	if _, err := verifiedPicks(raw); err == nil {
		t.Fatal("ambiguous mapping accepted")
	}
}
