package pan115

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/SheltonZhu/115driver/pkg/driver"
)

type fixtureUploadCipher struct{}

func (fixtureUploadCipher) Encrypt(b []byte) ([]byte, error)  { return b, nil }
func (fixtureUploadCipher) Decrypt(b []byte) ([]byte, error)  { return b, nil }
func (fixtureUploadCipher) EncodeToken(int64) (string, error) { return "fixture-key", nil }

func TestCookieUploadVersionDiscovery(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"state":true,"data":{"win":{"version_code":"36.8.2"}}}`, "36.8.2"},
		{`{"state":false}`, cookieUploadVersion},
		{`{"state":true,"data":{"win":{"version_code":"invalid"}}}`, cookieUploadVersion},
	} {
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != driver.ApiGetVersion {
				t.Fatalf("unexpected version endpoint: %s", req.URL)
			}
			return jsonResponse(tc.body), nil
		})}
		if got := currentUploadVersion(context.Background(), client); got != tc.want {
			t.Fatalf("version %q, want %q", got, tc.want)
		}
	}
}

func TestCookieUploadUsesCurrentVersionForFormTokenAndRangeChallenge(t *testing.T) {
	requests := 0
	c := driver.New(driver.WithClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		b, _ := io.ReadAll(req.Body)
		form, err := url.ParseQuery(string(b))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("appversion") != "36.0.1" || !strings.Contains(req.UserAgent(), "36.0.1") {
			t.Fatalf("old version in form/header: %v", form)
		}
		if form.Get("token") == cookieUploadToken("123", "SHA1", "6", form.Get("t"), form.Get("sign_key"), form.Get("sign_val"), "27.0.5.7") {
			t.Fatal("token still uses old version")
		}
		if requests == 1 {
			return jsonResponse(`{"status":7,"statuscode":701,"sign_key":"range-key","sign_check":"1-3"}`), nil
		}
		h := sha1.Sum([]byte("bcd"))
		if form.Get("sign_key") != "range-key" || form.Get("sign_val") != strings.ToUpper(hex.EncodeToString(h[:])) {
			t.Fatalf("wrong range digest: %v", form)
		}
		return jsonResponse(`{"status":2,"statuscode":0,"pickcode":"remote-pick"}`), nil
	})}))
	c.UserID, c.Userkey = 123, "fixture-userkey"
	result, err := initCookieUpload(context.Background(), c, strings.NewReader("abcdef"), "root", "movie.mkv", 6, "SHA1", "36.0.1", fixtureUploadCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if rapid, err := result.Ok(); err != nil || !rapid || result.SHA1 != "SHA1" || requests != 2 {
		t.Fatalf("unexpected result: %#v, %v", result, err)
	}
}

func TestCookieUploadRejectsInvalidRangeAndDistinguishesDataTransfer(t *testing.T) {
	for _, tc := range []struct {
		body        string
		fail, rapid bool
	}{
		{`{"status":7,"statuscode":701,"sign_key":"key","sign_check":"0-6"}`, true, false},
		{`{"status":1,"statuscode":0,"bucket":"bucket","object":"object"}`, false, false},
		{`{"status":2,"statuscode":0}`, false, true},
	} {
		c := driver.New(driver.WithClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(tc.body), nil })}))
		result, err := initCookieUpload(context.Background(), c, strings.NewReader("abcdef"), "root", "movie.mkv", 6, "SHA1", cookieUploadVersion, fixtureUploadCipher{})
		if tc.fail {
			if err == nil {
				t.Fatal("invalid challenge accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		rapid, err := result.Ok()
		if err != nil || rapid != tc.rapid {
			t.Fatalf("wrong upload type: rapid=%v, %v", rapid, err)
		}
	}
}

func TestCookieMultipartStreamsPartsAndCompletesWithCallback(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "video")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const size = 17 << 20
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	parts, total, completed := 0, int64(0), false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == driver.ApiUploadOSSToken {
			return jsonResponse(`{"StatusCode":"200","AccessKeyID":"key","AccessKeySecret":"secret","SecurityToken":"security"}`), nil
		}
		var body string
		switch req.Method {
		case http.MethodPost:
			if req.URL.Query().Has("uploads") {
				body = `<InitiateMultipartUploadResult><Bucket>fixture-bucket</Bucket><Key>fixture-object</Key><UploadId>fixture-id</UploadId></InitiateMultipartUploadResult>`
			} else {
				if req.Header.Get("x-oss-callback") == "" {
					t.Fatal("missing 115 callback on completion")
				}
				completed = true
				body = `<CompleteMultipartUploadResult><Location>fixture</Location><Bucket>fixture-bucket</Bucket><Key>fixture-object</Key><ETag>done</ETag></CompleteMultipartUploadResult>`
			}
		case http.MethodPut:
			parts++
			n, err := io.Copy(io.Discard, req.Body)
			if err != nil {
				t.Fatal(err)
			}
			total += n
			if req.Header.Get("x-oss-security-token") != "security" {
				t.Fatal("missing security token")
			}
		default:
			t.Fatalf("unexpected OSS method %s", req.Method)
		}
		resp := jsonResponse(body)
		resp.Header.Set("Content-Type", "application/xml")
		resp.Header.Set("ETag", fmt.Sprint(parts))
		return resp, nil
	})}
	c := driver.New(driver.WithClient(client))
	params := &driver.UploadOSSParams{Bucket: "fixture-bucket", Object: "fixture-object"}
	params.Callback.Callback = `{"callbackUrl":"https://fixture.invalid"}`
	if err := uploadCookieMultipart(context.Background(), c, client, file, params, "fixture.mkv", size); err != nil {
		t.Fatal(err)
	}
	if total != size || parts != 2 || !completed {
		t.Fatalf("parts=%d bytes=%d completed=%v", parts, total, completed)
	}
}
