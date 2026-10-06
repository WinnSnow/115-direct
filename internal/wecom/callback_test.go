package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type unreadCallbackBody struct{ read bool }

func (b *unreadCallbackBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *unreadCallbackBody) Close() error             { return nil }

func encryptCallback(t *testing.T, key []byte, message, corp string) string {
	t.Helper()
	plain := append(bytes.Repeat([]byte{1}, 16), make([]byte, 4)...)
	binary.BigEndian.PutUint32(plain[16:20], uint32(len(message)))
	plain = append(plain, []byte(message+corp)...)
	pad := 32 - len(plain)%32
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(encrypted, plain)
	return base64.StdEncoding.EncodeToString(encrypted)
}

func TestCallbackEntryTokenPrecedesBodyAndWeComVerification(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	cfg := Config{Enabled: true, CorpID: "corp-test", Token: "signature-token", EncodingAESKey: strings.TrimRight(base64.StdEncoding.EncodeToString(key), "="), CallbackAccessToken: strings.Repeat("a", 64)}
	h := NewHandler(nil, nil, func(context.Context) (Config, error) { return cfg, nil })
	for _, query := range []string{"", "access_token=wrong", "access_token=" + cfg.CallbackAccessToken + "&access_token=wrong"} {
		for _, method := range []string{"GET", "POST"} {
			body := &unreadCallbackBody{}
			r := httptest.NewRequest(method, "/callbacks/wecom?"+query, nil)
			r.Body = body
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 || body.read {
				t.Fatalf("%s token gate: status=%d body_read=%v", method, w.Code, body.read)
			}
		}
	}
	request := func(method, encrypted, signature, body string) *httptest.ResponseRecorder {
		q := url.Values{"access_token": {cfg.CallbackAccessToken}, "timestamp": {"1"}, "nonce": {"2"}, "msg_signature": {signature}}
		if method == "GET" {
			q.Set("echostr", encrypted)
		}
		r := httptest.NewRequest(method, "/callbacks/wecom?"+q.Encode(), strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	echo := encryptCallback(t, key, "verify-echo", cfg.CorpID)
	if w := request("GET", echo, Signature(cfg.Token, "1", "2", echo), ""); w.Code != 200 || w.Body.String() != "verify-echo" {
		t.Fatalf("valid handshake: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", echo, "wrong", ""); w.Code != 403 {
		t.Fatal("entry token bypassed WeCom signature")
	}
	message := encryptCallback(t, key, `<xml><MsgType>event</MsgType></xml>`, cfg.CorpID)
	body := "<xml><Encrypt>" + message + "</Encrypt></xml>"
	if w := request("POST", message, Signature(cfg.Token, "1", "2", message), body); w.Code != 200 || w.Body.String() != "success" {
		t.Fatalf("valid message: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", message, "wrong", body); w.Code != 403 {
		t.Fatal("entry token bypassed message signature")
	}
	wrongCorp := encryptCallback(t, key, "verify-echo", "other-corp")
	if w := request("GET", wrongCorp, Signature(cfg.Token, "1", "2", wrongCorp), ""); w.Code != 400 {
		t.Fatal("corp mismatch accepted")
	}
	if w := request("POST", "", "", "plain request"); w.Code != 400 {
		t.Fatal("malformed body accepted")
	}
	if w := request("POST", "", "", strings.Repeat("a", (2<<20)+1)); w.Code != 413 {
		t.Fatal("oversized body accepted")
	}
	old := cfg.CallbackAccessToken
	cfg.CallbackAccessToken = strings.Repeat("b", 64)
	r := httptest.NewRequest("GET", "/callbacks/wecom?access_token="+old, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("old token survived rotation")
	}
	cfg.CallbackAccessToken = ""
	if w := request("GET", echo, Signature(cfg.Token, "1", "2", echo), ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("missing configured gate accepted")
	}
}

func TestCallbackConfigValidationAndURL(t *testing.T) {
	valid := Config{Enabled: true, CallbackAccessToken: strings.Repeat("a", 64), CallbackBaseURL: "https://direct.example.com/prefix/"}
	address, err := valid.CallbackURL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(address)
	if err != nil || u.Path != "/prefix/callbacks/wecom" || u.Query().Get("access_token") != valid.CallbackAccessToken {
		t.Fatal("invalid callback URL")
	}
	for _, token := range []string{"", "short", strings.Repeat("x", 129), strings.Repeat("x", 32) + "&other=1"} {
		c := valid
		c.CallbackAccessToken = token
		if c.Validate() == nil {
			t.Fatal("invalid access token accepted")
		}
	}
	for _, base := range []string{"javascript:alert(1)", "https://user:password@host", "https://host?secret=x", "https://host#fragment", "https://", "//host"} {
		c := valid
		c.CallbackBaseURL = base
		if c.Validate() == nil {
			t.Fatal("invalid base URL accepted")
		}
	}
	if (Config{}).Validate() != nil {
		t.Fatal("disabled unconfigured integration rejected")
	}
}

func TestShareMessageSettingsValidation(t *testing.T) {
	for _, policy := range []string{"", "updates", "skip"} {
		for _, minutes := range []int{0, 10, 1440} {
			if err := (Config{ShareRepeatPolicy: policy, ShareCheckMinutes: &minutes}).Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if (Config{ShareRepeatPolicy: "invalid"}).Validate() == nil {
		t.Fatal("unknown repeat policy accepted")
	}
	for _, minutes := range []int{-1, 1441} {
		if (Config{ShareCheckMinutes: &minutes}).Validate() == nil {
			t.Fatal("invalid check interval accepted")
		}
	}
}
