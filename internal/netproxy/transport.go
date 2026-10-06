package netproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/local/115-direct/internal/store"
)

type Config struct {
	Enabled       bool     `json:"enabled"`
	URL           string   `json:"url"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	TMDB          bool     `json:"tmdb"`
	Images        bool     `json:"images"`
	Pan           bool     `json:"pan"`
	WeCom         bool     `json:"wecom"`
	Jellyfin      bool     `json:"jellyfin"`
	BypassPrivate bool     `json:"bypass_private"`
	Bypass        []string `json:"bypass"`
}

func Defaults() Config {
	return Config{TMDB: true, Images: true, BypassPrivate: true, Bypass: []string{}}
}
func (c Config) Validate() error {
	if c.URL == "" && !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("代理地址必须仅含协议、主机和端口，认证请单独填写")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return fmt.Errorf("代理协议须为 HTTP、HTTPS 或 SOCKS5")
	}
	return nil
}

type Transport struct {
	Store     *store.Store
	Service   string
	mu        sync.Mutex
	key       string
	transport *http.Transport
}

func New(st *store.Store, service string) *Transport { return &Transport{Store: st, Service: service} }
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := Defaults()
	if err := t.Store.GetSetting(r.Context(), "proxy", &c); err != nil && !store.IsMissingSetting(err) {
		return nil, fmt.Errorf("代理配置读取失败")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(c)
	key := string(raw)
	t.mu.Lock()
	if t.transport == nil || t.key != key {
		if t.transport != nil {
			t.transport.CloseIdleConnections()
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		enabled := c.TMDB
		if t.Service == "images" {
			enabled = c.Images
		}
		if t.Service == "pan" {
			enabled = c.Pan
		}
		if t.Service == "wecom" {
			enabled = c.WeCom
		}
		if t.Service == "jellyfin" {
			enabled = c.Jellyfin
		}
		if c.Enabled && enabled {
			u, _ := url.Parse(c.URL)
			if c.Username != "" || c.Password != "" {
				u.User = url.UserPassword(c.Username, c.Password)
			}
			tr.Proxy = func(req *http.Request) (*url.URL, error) {
				if bypass(req.Context(), req.URL.Hostname(), c) {
					return nil, nil
				}
				return u, nil
			}
		}
		t.transport = tr
		t.key = key
	}
	tr := t.transport
	t.mu.Unlock()
	return tr.RoundTrip(r)
}
func bypass(ctx context.Context, host string, c Config) bool {
	host = strings.ToLower(host)
	if c.BypassPrivate {
		if host == "localhost" || !strings.Contains(host, ".") {
			return true
		}
		ip := net.ParseIP(host)
		if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			return true
		}
	}
	for _, rule := range c.Bypass {
		rule = strings.ToLower(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		if host == rule || strings.HasSuffix(host, "."+strings.TrimPrefix(rule, ".")) {
			return true
		}
		_, network, err := net.ParseCIDR(rule)
		if err == nil {
			ip := net.ParseIP(host)
			if ip != nil && network.Contains(ip) {
				return true
			}
		}
	}
	return false
}
