package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	URL      string `json:"url"`
	APIKey   string `json:"api_key"`
	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	BasePath string `json:"base_path,omitempty"`
}

// Resolve retains the legacy URL when no explicit host is configured.
func (c *Config) Resolve() error {
	if strings.TrimSpace(c.Host) != "" {
		if c.Protocol != "http" && c.Protocol != "https" {
			return fmt.Errorf("Jellyfin协议须为http或https")
		}
		host := strings.Trim(strings.TrimSpace(c.Host), "[]")
		if strings.ContainsAny(host, "/ ?#@\\\r\n") || c.Port < 1 || c.Port > 65535 {
			return fmt.Errorf("Jellyfin主机或端口无效")
		}
		if c.BasePath != "" && (!strings.HasPrefix(c.BasePath, "/") || strings.ContainsAny(c.BasePath, "?#\\") || strings.Contains(c.BasePath, "..")) {
			return fmt.Errorf("Jellyfin基础路径无效")
		}
		c.URL = (&url.URL{Scheme: c.Protocol, Host: net.JoinHostPort(host, strconv.Itoa(c.Port)), Path: strings.TrimRight(c.BasePath, "/")}).String()
	}
	if c.URL == "" {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("Jellyfin原始后端地址无效")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return nil
}

type Client struct {
	HTTP   *http.Client
	Config func(context.Context) (Config, error)
}

func NewClient(loader func(context.Context) (Config, error)) *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, Config: loader}
}

func (c *Client) Refresh(ctx context.Context) error {
	cfg, err := c.Config(ctx)
	if err == nil {
		err = cfg.Resolve()
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("Jellyfin is not configured")
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/Library/Refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	setAuthHeaders(req, cfg.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Jellyfin refresh HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

func (c *Client) Test(ctx context.Context) (map[string]any, error) {
	cfg, err := c.Config(ctx)
	if err == nil {
		err = cfg.Resolve()
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("Jellyfin 地址或 API Key 尚未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.URL, "/")+"/System/Info", nil)
	if err != nil {
		return nil, err
	}
	setAuthHeaders(req, cfg.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("Jellyfin API Key 被拒绝，请使用控制台中创建的 API 密钥")
		}
		return nil, fmt.Errorf("Jellyfin HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	return out, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

func setAuthHeaders(req *http.Request, token string) {
	token = strings.TrimSpace(token)
	replacer := strings.NewReplacer("\\", "", "\"", "", "\r", "", "\n", "")
	token = replacer.Replace(token)
	req.Header.Set("Authorization", `MediaBrowser Client="115 Direct", Device="Gateway", DeviceId="115-direct", Version="0.1.0", Token="`+token+`"`)
	req.Header.Set("X-Emby-Token", token)
}
