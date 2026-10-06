package wecom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/secure"
	"github.com/local/115-direct/internal/store"
)

type messagePan struct {
	pan115.Provider
	files  []pan115.ShareFile
	checks int
}

func (p *messagePan) SnapshotShareTree(_ context.Context, link, code string) (*pan115.ShareSnapshot, error) {
	p.checks++
	if code != "fx1234" || strings.Contains(link, "?") {
		return nil, fmt.Errorf("password parsing or canonical URL failed")
	}
	return &pan115.ShareSnapshot{Code: "abc", Title: "Show", Files: append([]pan115.ShareFile(nil), p.files...)}, nil
}

type messageTransport func(*http.Request) (*http.Response, error)

func (f messageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWeComMessageRepliesAndShareRecognitionSettings(t *testing.T) {
	dir := t.TempDir()
	v, err := secure.LoadOrCreate(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &messagePan{files: []pan115.ShareFile{{Relative: "Show.S01E01.mkv", Entry: pan115.Entry{ID: "one", Name: "Show.S01E01.mkv", SHA1: "one", Size: 1}}}}
	jobs := organize.NewService(st, p, nil, nil, organize.DirectoryConfig{})
	cfg := Config{Enabled: true, ShareRepeatPolicy: "skip"}
	h := NewHandler(st, jobs, func(context.Context) (Config, error) { return cfg, nil })
	replies := []string{}
	h.HTTP = &http.Client{Transport: messageTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"access_token":"fixture-access","expires_in":7200}`
		if r.URL.Path == "/cgi-bin/message/send" {
			var payload struct {
				ToUser string `json:"touser"`
				Text   struct {
					Content string `json:"content"`
				} `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.ToUser != "user1" {
				t.Fatal("wrong recipient")
			}
			replies = append(replies, payload.Text.Content)
			body = `{"errcode":0}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	h.handleText("user1", "https://115.com/s/abc?password=fx1234 tmdb:tv:229192")
	if len(replies) != 1 || !strings.Contains(replies[0], "已创建转存任务") {
		t.Fatal("first reply wrong")
	}
	h.handleText("user1", "https://115cdn.com/s/abc 提取码：fx1234")
	if !strings.Contains(replies[1], "正在处理") || p.checks != 1 {
		t.Fatal("active share re-created")
	}
	first, err := st.ShareJob(context.Background(), "https://115.com/s/abc")
	if err != nil {
		t.Fatal(err)
	}
	first.Status = "completed"
	if err := st.UpdateJob(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	h.handleText("user1", "https://115.com/s/abc 密码:fx1234")
	if !strings.Contains(replies[2], "已转存，无需重复发送") || p.checks != 1 {
		t.Fatal("duplicate completion reply wrong")
	}
	p.files = append(p.files, pan115.ShareFile{Relative: "Show.S01E02.mkv", Entry: pan115.Entry{ID: "two", Name: "Show.S01E02.mkv", Size: 2}})
	h.handleText("user1", "检查更新 https://115.com/s/abc?password=fx1234")
	if !strings.Contains(replies[3], "检测到分享更新") || p.checks != 2 {
		t.Fatal("force check or update reply missing")
	}
	disabled := false
	cfg.ShareEnabled = &disabled
	h.handleText("user1", "https://115.com/s/abc?password=fx1234")
	if !strings.Contains(replies[4], "已关闭") || p.checks != 2 {
		t.Fatal("recognition switch ignored")
	}
}

func TestEmptyAllowUsersAllowsVisibleMembers(t *testing.T) {
	if !allowed(nil, "member-a") || !allowed([]string{}, "member-b") {
		t.Fatal("empty UserID allowlist should allow visible members")
	}
	if allowed([]string{"member-a"}, "member-b") || !allowed([]string{"member-a"}, "member-a") {
		t.Fatal("non-empty UserID allowlist is not enforced")
	}
}
