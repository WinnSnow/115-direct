package jellyfin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var cmsFilePattern = regexp.MustCompile(`^/d/([A-Za-z0-9]{6,64})(?:\.([A-Za-z0-9]{1,8}))?$`)

type CMSMediaSource struct {
	URL       string `json:"url"`
	PickCode  string `json:"pick_code"`
	Extension string `json:"extension,omitempty"`
}

type CMSClaims struct {
	Source    CMSMediaSource `json:"source"`
	Extension string         `json:"extension"`
	TokenHash string         `json:"token_hash"`
	Expires   int64          `json:"expires"`
}

// CMSBridge recognizes legacy URLs. It either uses a fixed CMS upstream or an
// explicitly configured independent resolver; neither mode proxies video.
type CMSBridge struct {
	upstream     *url.URL
	origins      map[string]bool
	client       *http.Client
	resolver     *Gateway
	mu           sync.Mutex
	requests     int
	maximum      int
	interval     time.Duration
	last         time.Time
	lastStatus   int
	independent  func(context.Context, string, string) (string, map[string]string, error)
	allowedPicks map[string]bool
	eligible     func(context.Context, string) bool
}

func (b *CMSBridge) Eligible(ctx context.Context, pick string) bool {
	if b.eligible != nil {
		return b.eligible(ctx, pick)
	}
	return b.independent == nil || b.allowedPicks[pick]
}

func NewCMSBridge(upstream string, origins []string, maximum int, interval time.Duration) (*CMSBridge, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("invalid CMS upstream")
	}
	b := &CMSBridge{upstream: u, origins: map[string]bool{}, maximum: maximum, interval: interval,
		client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, raw := range origins {
		origin, err := url.Parse(raw)
		if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" {
			return nil, fmt.Errorf("invalid CMS origin")
		}
		b.origins[strings.ToLower(origin.Scheme+"://"+origin.Host)] = true
	}
	b.resolver = NewGateway(nil, nil, nil, nil)
	b.resolver.linkLookup = b.lookup
	return b, nil
}

// NewIndependentCMSBridge has no CMS fallback. Only preverified pickcodes can
// reach the supplied resolver, including through the legacy /d/ test listener.
func NewIndependentCMSBridge(origins []string, picks []string, lookup func(context.Context, string, string) (string, map[string]string, error), maximum int, interval time.Duration) (*CMSBridge, error) {
	if lookup == nil || len(origins) == 0 || len(picks) == 0 || maximum <= 0 {
		return nil, fmt.Errorf("independent resolver and bounded sample scope required")
	}
	b, err := NewCMSBridge("http://127.0.0.1:1", origins, maximum, interval)
	if err != nil {
		return nil, err
	}
	b.upstream = nil
	b.client = nil
	b.independent = lookup
	b.allowedPicks = map[string]bool{}
	for _, pick := range picks {
		if !cmsFilePattern.MatchString("/d/"+pick) || strings.Contains(pick, ".") {
			return nil, fmt.Errorf("invalid sample pickcode")
		}
		b.allowedPicks[pick] = true
	}
	return b, nil
}

func (b *CMSBridge) Mode() string {
	if b.independent != nil {
		return "independent-115"
	}
	return "cms-upstream"
}

func (b *CMSBridge) ParseSource(raw string) (CMSMediaSource, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || len(raw) > 4096 || !b.origins[strings.ToLower(u.Scheme+"://"+u.Host)] {
		return CMSMediaSource{}, false
	}
	m := cmsFilePattern.FindStringSubmatch(u.Path)
	if len(m) != 3 || (m[2] != "" && mediaExtension("file."+strings.ToLower(m[2])) == "") {
		return CMSMediaSource{}, false
	}
	return CMSMediaSource{URL: raw, PickCode: m[1], Extension: strings.ToLower(m[2])}, true
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (b *CMSBridge) playPath(secret []byte, source CMSMediaSource, ext, token string) string {
	claims := CMSClaims{Source: source, Extension: ext, TokenHash: tokenHash(token), Expires: time.Now().Add(4 * time.Hour).Unix()}
	raw, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("cms|" + payload))
	ticket := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "/cms/play/" + source.PickCode + "." + ext + "?ticket=" + url.QueryEscape(ticket) + "&api_key=" + url.QueryEscape(token)
}

func (b *CMSBridge) play(g *Gateway, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := jellyfinToken(r)
	if err := g.authorize(r.Context(), token); err != nil {
		http.Error(w, "Jellyfin authorization required", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(r.URL.Query().Get("ticket"), ".")
	if len(parts) != 2 || len(parts[0]) > 8192 {
		http.Error(w, "invalid CMS ticket", 403)
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, g.Secret)
	mac.Write([]byte("cms|" + parts[0]))
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		http.Error(w, "invalid CMS ticket", 403)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	var claims CMSClaims
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Expires < time.Now().Unix() || claims.TokenHash != tokenHash(token) {
		http.Error(w, "invalid CMS ticket", 403)
		return
	}
	source, ok := b.ParseSource(claims.Source.URL)
	if !ok || source.PickCode != claims.Source.PickCode || mediaRoutePath(r.URL.Path) != "/cms/play/"+source.PickCode+"."+claims.Extension {
		http.Error(w, "CMS source mismatch", 403)
		return
	}
	result, err := b.resolver.resolveLink(r.Context(), source.URL, r.UserAgent(), false)
	id := "cms-" + tokenHash(source.URL)[:16]
	if err != nil {
		g.recordDelivery(r, id, "link_failed", 502, result, false)
		http.Error(w, "legacy media link request failed", 502)
		return
	}
	w.Header().Set("Location", result.URL)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-CMS-Link-Mode", b.Mode())
	w.WriteHeader(http.StatusFound)
	g.recordDelivery(r, id, "cdn_redirect", 302, result, false)
}

// ServeLegacyHTTP is for an isolated compatibility listener. It is intentionally
// sample-scoped and disabled in CMS-upstream mode. Production authentication and
// listener exposure must be configured separately before deploying this route.
func (b *CMSBridge) ServeLegacyHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	m := cmsFilePattern.FindStringSubmatch(r.URL.Path)
	if b.independent == nil || len(m) != 3 || !b.Eligible(r.Context(), m[1]) || (m[2] != "" && mediaExtension("file."+strings.ToLower(m[2])) == "") {
		http.NotFound(w, r)
		return
	}
	// The caller's Host and query never become an upstream destination.
	var origin string
	for candidate := range b.origins {
		origin = candidate
		break
	}
	result, err := b.resolver.resolveLink(r.Context(), origin+r.URL.EscapedPath(), r.UserAgent(), false)
	if err != nil {
		http.Error(w, "independent link request failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Location", result.URL)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-CMS-Link-Mode", b.Mode())
	w.WriteHeader(http.StatusFound)
}

func (b *CMSBridge) Requests() int { b.mu.Lock(); defer b.mu.Unlock(); return b.requests }

func (b *CMSBridge) LastStatus() int { b.mu.Lock(); defer b.mu.Unlock(); return b.lastStatus }

func (b *CMSBridge) lookup(ctx context.Context, raw, ua string) (string, map[string]string, error) {
	source, ok := b.ParseSource(raw)
	if !ok {
		return "", nil, fmt.Errorf("invalid CMS source")
	}
	if b.independent != nil && !b.Eligible(ctx, source.PickCode) {
		return "", nil, fmt.Errorf("pickcode outside verified sample scope")
	}
	// Jellyfin can return literal Chinese filenames in RawQuery. HTTP request
	// targets must encode those bytes while preserving existing percent escapes.
	b.mu.Lock()
	if b.maximum > 0 && b.requests >= b.maximum {
		b.mu.Unlock()
		return "", nil, fmt.Errorf("CMS acceptance request budget exhausted")
	}
	delay := time.Until(b.last.Add(b.interval))
	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			b.mu.Unlock()
			return "", nil, ctx.Err()
		case <-timer.C:
		}
	}
	b.last = time.Now()
	b.requests++
	b.mu.Unlock()
	if b.independent != nil {
		return b.independent(ctx, source.PickCode, ua)
	}
	original, _ := url.Parse(source.URL)
	target := *b.upstream
	target.Path = original.Path
	var query strings.Builder
	for _, c := range []byte(original.RawQuery) {
		if c <= 32 || c >= 127 {
			fmt.Fprintf(&query, "%%%02X", c)
		} else {
			query.WriteByte(c)
		}
	}
	target.RawQuery = query.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := b.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("CMS upstream connection failed")
	}
	defer resp.Body.Close()
	b.mu.Lock()
	b.lastStatus = resp.StatusCode
	b.mu.Unlock()
	if resp.StatusCode != http.StatusFound {
		return "", nil, fmt.Errorf("CMS upstream HTTP %d, expected 302", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return "", nil, fmt.Errorf("CMS upstream has no Location")
	}
	return location, map[string]string{"User-Agent": ua}, nil
}
