package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/netproxy"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

// CMSConfig only describes legacy media; CMS itself is never contacted.
type CMSConfig struct {
	Enabled           bool     `json:"enabled"`
	Origins           []string `json:"origins"`
	STRMRoots         []string `json:"strm_roots"`
	Cookie            string   `json:"cookie"`
	LegacyEnabled     bool     `json:"legacy_enabled"`
	MigrationReadOnly bool     `json:"migration_read_only"`
	IntervalMS        int      `json:"interval_ms"`
}

func DefaultCMSConfig() CMSConfig {
	return CMSConfig{Origins: []string{}, STRMRoots: []string{}, IntervalMS: 1000}
}
func (c *CMSConfig) Normalize() {
	if c.Origins == nil {
		c.Origins = []string{}
	}
	if c.STRMRoots == nil {
		c.STRMRoots = []string{}
	}
	if c.IntervalMS == 0 {
		c.IntervalMS = 1000
	}
	for i, v := range c.Origins {
		c.Origins[i] = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	for i, v := range c.STRMRoots {
		c.STRMRoots[i] = strings.TrimSpace(v)
	}
}
func (c CMSConfig) Validate() error {
	if c.IntervalMS < 200 || c.IntervalMS > 60000 {
		return fmt.Errorf("115请求间隔须为200至60000毫秒")
	}
	if len(c.Origins) > 32 || len(c.STRMRoots) > 32 {
		return fmt.Errorf("旧地址和继承根目录最多各32项")
	}
	for _, raw := range c.Origins {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("旧播放地址须为完整HTTP/HTTPS源地址，不包含路径、查询或认证信息")
		}
	}
	for _, root := range c.STRMRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
			return fmt.Errorf("继承目录须为绝对路径，且不得为根目录")
		}
	}
	if c.Enabled && (len(c.Origins) == 0 || strings.TrimSpace(c.Cookie) == "") {
		return fmt.Errorf("启用CMS接管前请配置旧播放地址及复用Cookie")
	}
	if c.LegacyEnabled && !c.Enabled {
		return fmt.Errorf("旧/d播放入口需要先启用CMS接管")
	}
	return nil
}

// CMSReadTransport permits only Cookie status and download URL lookup. Imported
// credentials cannot initiate login, SSO, cloud writes, or redirect to other APIs.
type CMSReadTransport struct{ Base http.RoundTripper }

func (t CMSReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	allowed := false
	if r.URL.Scheme == "https" && r.URL.User == nil && r.URL.Port() == "" {
		if r.Method == http.MethodPost && r.URL.Host == "proapi.115.com" && r.URL.Path == "/app/chrome/downurl" && r.URL.RawQuery == "" {
			allowed = true
		}
		if r.Method == http.MethodGet && r.URL.Host == "my.115.com" && r.URL.Path == "/" {
			q := r.URL.Query()
			allowed = len(q["ct"]) == 1 && len(q["ac"]) == 1 && q.Get("ct") == "guide" && q.Get("ac") == "status"
			for k := range q {
				if k != "ct" && k != "ac" && k != "_" {
					allowed = false
				}
			}
		}
	}
	if !allowed {
		return nil, fmt.Errorf("CMS复用Cookie仅允许只读状态和直链接口")
	}
	return t.Base.RoundTrip(r)
}

type CMSRuntime struct {
	Store  *store.Store
	mu     sync.Mutex
	loaded time.Time
	bridge *CMSBridge
	config CMSConfig
	err    error
}

func (m *CMSRuntime) Invalidate() { m.mu.Lock(); m.loaded = time.Time{}; m.mu.Unlock() }
func (m *CMSRuntime) Bridge(ctx context.Context) *CMSBridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.loaded) < 2*time.Second {
		return m.bridge
	}
	m.loaded = time.Now()
	cfg := DefaultCMSConfig()
	err := m.Store.GetSetting(ctx, "cms", &cfg)
	if err != nil {
		m.bridge = nil
		return nil
	}
	cfg.Normalize()
	if m.bridge != nil && sameCMSConfig(cfg, m.config) {
		return m.bridge
	}
	m.config = cfg
	m.bridge = nil
	m.err = nil
	if !cfg.Enabled {
		return nil
	}
	if err = cfg.Validate(); err != nil {
		m.err = err
		return nil
	}
	pan := pan115.New()
	pan.ConfigureHTTP(&http.Client{Transport: CMSReadTransport{Base: netproxy.New(m.Store, "pan")}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	if err = pan.SetCookie(cfg.Cookie); err != nil {
		m.err = fmt.Errorf("CMS复用Cookie格式无效")
		return nil
	}
	b, err := NewCMSBridge("http://127.0.0.1:1", cfg.Origins, 0, time.Duration(cfg.IntervalMS)*time.Millisecond)
	if err != nil {
		m.err = err
		return nil
	}
	b.upstream = nil
	b.client = nil
	b.independent = pan.DownloadURL
	b.eligible = func(ctx context.Context, pick string) bool {
		ok, err := m.Store.HasCMSPick(ctx, pick)
		return err == nil && ok
	}
	m.bridge = b
	return b
}
func sameCMSConfig(a, b CMSConfig) bool {
	return a.Enabled == b.Enabled && a.Cookie == b.Cookie && a.IntervalMS == b.IntervalMS && strings.Join(a.Origins, "\n") == strings.Join(b.Origins, "\n")
}
func (m *CMSRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b := m.Bridge(r.Context())
	var cfg CMSConfig
	if b == nil || m.Store.GetSetting(r.Context(), "cms", &cfg) != nil || !cfg.LegacyEnabled {
		http.NotFound(w, r)
		return
	}
	b.ServeLegacyHTTP(w, r)
}
func (m *CMSRuntime) Check(ctx context.Context) error {
	var cfg CMSConfig
	if err := m.Store.GetSetting(ctx, "cms", &cfg); err != nil || cfg.Cookie == "" {
		return fmt.Errorf("尚未配置CMS复用Cookie")
	}
	pan := pan115.New()
	pan.ConfigureHTTP(&http.Client{Transport: CMSReadTransport{Base: netproxy.New(m.Store, "pan")}, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	if pan.SetCookie(cfg.Cookie) != nil {
		return fmt.Errorf("CMS复用Cookie格式无效")
	}
	if pan.Check(ctx) != nil {
		return fmt.Errorf("复用Cookie检查失败，请手动更新；未执行登录操作")
	}
	return nil
}
