package pan115

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var messageShareRE = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:115\.com|115cdn\.com|anxia\.com)/s/[A-Za-z0-9_-]+(?:\?[^\s<>"'，。；、）】》]*)?`)
var messagePasswordRE = regexp.MustCompile(`(?i)(?:提取码|提取碼|访问码|訪問碼|密码|密碼|password|pwd|receive_code)\s*[:：=]?\s*([A-Za-z0-9]+)`)

type ShareMessage struct{ URL, Code string }

func ParseShareMessage(text string) (ShareMessage, error) {
	text = html.UnescapeString(text)
	links := messageShareRE.FindAllString(text, -1)
	if len(links) == 0 {
		return ShareMessage{}, fmt.Errorf("未找到有效的115分享链接")
	}
	if len(links) > 1 {
		return ShareMessage{}, fmt.Errorf("请每条消息发送一个115分享链接，便于对应提取码与转存结果")
	}
	link := strings.TrimRight(links[0], ".,;!)]}")
	u, err := url.Parse(link)
	if err != nil {
		return ShareMessage{}, fmt.Errorf("115分享链接格式无效")
	}
	code := ""
	for _, key := range []string{"password", "receive_code", "pwd"} {
		if v := strings.TrimSpace(u.Query().Get(key)); v != "" {
			code = v
			break
		}
	}
	if code == "" {
		if m := messagePasswordRE.FindStringSubmatch(text); len(m) == 2 {
			code = m[1]
		}
	}
	u.RawQuery = ""
	u.Fragment = ""
	return ShareMessage{URL: u.String(), Code: code}, nil
}
