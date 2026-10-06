package wecom

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var accessTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

func (c Config) Validate() error {
	if c.ShareRepeatPolicy != "" && c.ShareRepeatPolicy != "updates" && c.ShareRepeatPolicy != "skip" {
		return fmt.Errorf("重复分享策略须为检查更新或直接提示已转存")
	}
	if c.ShareCheckMinutes != nil && (*c.ShareCheckMinutes < 0 || *c.ShareCheckMinutes > 1440) {
		return fmt.Errorf("重复分享检查间隔须为0至1440分钟")
	}
	if c.MenuEnabled && !c.MenuOrganize && !c.MenuFullSync && !c.MenuIncrementalSync {
		return fmt.Errorf("启用企业微信应用菜单时至少选择一个按钮")
	}
	if c.Enabled && c.CallbackAccessToken == "" {
		return fmt.Errorf("启用企业微信前请配置回调入口 Token")
	}
	if c.CallbackAccessToken != "" && !accessTokenPattern.MatchString(c.CallbackAccessToken) {
		return fmt.Errorf("回调入口 Token 须为32至128位字母、数字、下划线或短横线，建议使用随机生成")
	}
	if c.CallbackBaseURL != "" {
		u, err := url.Parse(c.CallbackBaseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("公网管理地址须为有效的 HTTP/HTTPS 地址，不含账号、查询参数或片段")
		}
	}
	return nil
}

func (c Config) CallbackURL() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if c.CallbackBaseURL == "" || c.CallbackAccessToken == "" {
		return "", fmt.Errorf("请先保存公网管理地址与回调入口 Token")
	}
	u, err := url.Parse(c.CallbackBaseURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/callbacks/wecom"
	u.RawPath = ""
	u.RawQuery = url.Values{"access_token": {c.CallbackAccessToken}}.Encode()
	return u.String(), nil
}
