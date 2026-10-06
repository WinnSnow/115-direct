package pan115

import "testing"

func TestShareMessagePasswordsAndPunctuation(t *testing.T) {
	for _, c := range []struct{ text, url, code string }{
		{"电影 https://115.com/s/abc?password=1234", "https://115.com/s/abc", "1234"},
		{"https://www.115cdn.com/s/ABC?receive_code=aB12。", "https://www.115cdn.com/s/ABC", "aB12"},
		{"https://anxia.com/s/abc?pwd=12%33%34&amp;from=wechat", "https://anxia.com/s/abc", "1234"},
		{"分享：https://115.com/s/abc，提取码：A123", "https://115.com/s/abc", "A123"},
		{"https://115.com/s/abc\n密码: fx1234\ntmdb:tv:229192", "https://115.com/s/abc", "fx1234"},
		{"(https://115.com/s/abc?password=fx1234)", "https://115.com/s/abc", "fx1234"},
		{"https://115.com/s/abc?password=url1 提取码: text2", "https://115.com/s/abc", "url1"},
	} {
		m, err := ParseShareMessage(c.text)
		if err != nil || m.URL != c.url || m.Code != c.code {
			t.Fatalf("parse %q: %+v %v", c.text, m, err)
		}
	}
	for _, text := range []string{"hello", "https://example.com/s/abc", "https://115.com/s/abc https://115.com/s/def"} {
		if _, err := ParseShareMessage(text); err == nil {
			t.Fatal("ambiguous/invalid message accepted")
		}
	}
}
