package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/115-direct/internal/organize"
	"github.com/local/115-direct/internal/pan115"
	"github.com/local/115-direct/internal/store"
)

type cachedLink struct {
	URL         string
	Expires     time.Time
	Checked     time.Time
	ProbeStatus int
}
type flight struct {
	done   chan struct{}
	err    error
	result linkResult
}

type Gateway struct {
	Store            *store.Store
	Pan              pan115.Provider
	Secret           []byte
	Config           func(context.Context) (Config, error)
	mu               sync.Mutex
	cache            map[string]cachedLink
	flights          map[string]*flight
	authCache        map[string]time.Time
	HTTP             *http.Client
	BackendTransport http.RoundTripper
	BackendHTTP      *http.Client
	linkGate         chan struct{}
	events           []PlaybackEvent
	sources          map[string]string
	CMS              *CMSBridge
	CMSLoader        func(context.Context) *CMSBridge
	linkLookup       func(context.Context, string, string) (string, map[string]string, error)
}

func NewGateway(st *store.Store, pan pan115.Provider, secret []byte, loader func(context.Context) (Config, error)) *Gateway {
	return &Gateway{Store: st, Pan: pan, Secret: secret, Config: loader, cache: map[string]cachedLink{}, flights: map[string]*flight{}, authCache: map[string]time.Time{}, sources: map[string]string{}, linkGate: make(chan struct{}, 1), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (g *Gateway) cmsBridge(ctx context.Context) *CMSBridge {
	if g.CMSLoader != nil {
		return g.CMSLoader(ctx)
	}
	return g.CMS
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/gateway/health" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "running"})
		return
	}
	if strings.HasPrefix(mediaRoutePath(r.URL.Path), "/cms/play/") {
		b := g.cmsBridge(r.Context())
		if b == nil {
			http.NotFound(w, r)
			return
		}
		b.play(g, w, r)
		return
	}
	if strings.HasPrefix(mediaRoutePath(r.URL.Path), "/direct/") {
		g.direct(w, r)
		return
	}
	cfg, err := g.Config(r.Context())
	if err != nil || cfg.URL == "" {
		http.Error(w, "Jellyfin is not configured", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(cfg.URL)
	if err != nil {
		http.Error(w, "invalid Jellyfin URL", http.StatusServiceUnavailable)
		return
	}
	if g.tryDirectMediaRequest(w, r, cfg) {
		return
	}
	g.observePlayback(r)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = g.BackendTransport
	baseDirector := proxy.Director
	playbackInfo := strings.Contains(strings.ToLower(r.URL.Path), "/playbackinfo")
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.Host = target.Host
		if playbackInfo {
			// ModifyResponse needs JSON, not a compressed backend response.
			req.Header.Set("Accept-Encoding", "identity")
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		slog.Error("Jellyfin proxy", "error", err)
		http.Error(w, "Jellyfin unavailable", http.StatusBadGateway)
	}
	if playbackInfo {
		proxy.ModifyResponse = func(resp *http.Response) error { return g.rewritePlaybackInfoFor(resp, r) }
	}
	proxy.ServeHTTP(w, r)
}

func (g *Gateway) rewritePlaybackInfo(resp *http.Response) error {
	return g.rewritePlaybackInfoFor(resp, resp.Request)
}

func (g *Gateway) rewritePlaybackInfoFor(resp *http.Response, viewer *http.Request) error {
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return fmt.Errorf("invalid playback information")
	}
	sources, _ := payload["MediaSources"].([]any)
	changed := false
	token := jellyfinToken(resp.Request)
	for _, raw := range sources {
		source, _ := raw.(map[string]any)
		path, _ := source["Path"].(string)
		if bridge := g.cmsBridge(viewer.Context()); bridge != nil {
			if cms, ok := bridge.ParseSource(path); ok && bridge.Eligible(viewer.Context(), cms.PickCode) {
				if err := g.authorize(viewer.Context(), token); err != nil {
					return fmt.Errorf("CMS playback requires Jellyfin permission")
				}
				ext := cms.Extension
				if ext == "" {
					ext, _ = source["Container"].(string)
				}
				if mediaExtension("file."+ext) == "" {
					ext = "mkv"
				}
				direct := bridge.playPath(g.Secret, cms, ext, token)
				source["Path"], source["DirectStreamUrl"] = g.viewerBase(viewer)+direct, direct
				source["SupportsDirectPlay"], source["SupportsDirectStream"] = true, true
				source["SupportsTranscoding"] = false
				source["Protocol"], source["IsRemote"] = "Http", true
				source["RequiresOpening"], source["RequiresClosing"] = false, false
				for _, key := range []string{"TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol", "OpenToken", "LiveStreamId"} {
					delete(source, key)
				}
				g.rememberSource(viewer.URL.Path, source, "cms-"+tokenHash(cms.URL)[:16])
				changed = true
				continue
			}
		}
		id, sig, ok := directIdentity(path)
		if !ok {
			continue
		}
		if !organize.VerifyMedia(g.Secret, id, sig) {
			return fmt.Errorf("invalid project media signature")
		}
		media, err := g.Store.GetMedia(viewer.Context(), id)
		if err != nil {
			return fmt.Errorf("project media not found")
		}
		direct := "/direct/" + url.PathEscape(id) + "?sig=" + url.QueryEscape(sig)
		if ext := mediaExtension(media.Name); ext != "" {
			direct = "/direct/" + url.PathEscape(id) + "/file." + ext + "?sig=" + url.QueryEscape(sig)
			source["Container"] = ext
		}
		if token != "" {
			direct += "&api_key=" + url.QueryEscape(token)
		}
		source["SupportsDirectPlay"], source["SupportsDirectStream"] = true, true
		source["SupportsTranscoding"] = false
		delete(source, "TranscodingUrl")
		delete(source, "TranscodingContainer")
		delete(source, "TranscodingSubProtocol")
		source["Path"], source["DirectStreamUrl"] = g.viewerBase(viewer)+direct, direct
		source["Protocol"], source["IsRemote"] = "Http", true
		source["RequiresOpening"], source["RequiresClosing"] = false, false
		delete(source, "OpenToken")
		delete(source, "LiveStreamId")
		g.rememberSource(viewer.URL.Path, source, id)
		changed = true
	}
	if changed {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Del("ETag")
	return nil
}

func directIdentity(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	index := strings.Index(u.Path, "/direct/")
	if index < 0 {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(u.Path[index:], "/direct/"), "/"), "/")
	id := parts[0]
	if id == "" || len(parts) > 2 || (len(parts) == 2 && (parts[1] != "file."+mediaExtension(parts[1]) || mediaExtension(parts[1]) == "")) {
		return "", "", false
	}
	return id, u.Query().Get("sig"), u.Query().Get("sig") != ""
}

func (g *Gateway) direct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, sig, ok := directIdentity(r.URL.String())
	if !ok || !organize.VerifyMedia(g.Secret, id, sig) {
		http.Error(w, "invalid media signature", http.StatusForbidden)
		return
	}
	g.redirectMedia(w, r, id)
}

func (g *Gateway) redirectMedia(w http.ResponseWriter, r *http.Request, id string) {
	if err := g.authorizeDirectRequest(r, id); err != nil {
		g.recordDelivery(r, id, "authorization_failed", http.StatusUnauthorized, linkResult{Stage: "authorization"}, false)
		http.Error(w, "Jellyfin authorization required", http.StatusUnauthorized)
		return
	}
	media, err := g.Store.GetMedia(r.Context(), id)
	if err != nil {
		g.recordDelivery(r, id, "media_not_found", http.StatusNotFound, linkResult{Stage: "media_lookup"}, false)
		http.NotFound(w, r)
		return
	}
	options, err := g.playbackOptions(r.Context())
	if err != nil {
		http.Error(w, "playback configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	result, err := g.resolveLink(r.Context(), media.PickCode, r.UserAgent(), options.CheckLink)
	if err != nil {
		g.recordDelivery(r, id, "link_failed", http.StatusBadGateway, result, isBackendProbe(r))
		http.Error(w, "cannot create direct link", http.StatusBadGateway)
		return
	}
	location := result.URL
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Vary", "User-Agent")
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
	g.recordDelivery(r, id, "cdn_redirect", http.StatusFound, result, isBackendProbe(r))
}

func (g *Gateway) authorizeDirectRequest(r *http.Request, id string) error {
	if err := g.authorize(r.Context(), jellyfinToken(r)); err == nil {
		return nil
	}
	if !organize.VerifyJellyfinRequest(g.Secret, id, r.URL.Query().Get("jellyfin_sig")) {
		return fmt.Errorf("missing Jellyfin backend signature")
	}
	if !g.isConfiguredJellyfinHost(r.Context(), r.RemoteAddr) {
		return fmt.Errorf("request did not originate from Jellyfin")
	}
	return nil
}

func (g *Gateway) isConfiguredJellyfinHost(ctx context.Context, remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	remoteIP := net.ParseIP(host)
	if remoteIP == nil {
		return false
	}
	cfg, err := g.Config(ctx)
	if err != nil {
		return false
	}
	target, err := url.Parse(cfg.URL)
	if err != nil || target.Hostname() == "" {
		return false
	}
	if targetIP := net.ParseIP(target.Hostname()); targetIP != nil {
		return targetIP.Equal(remoteIP)
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", target.Hostname())
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if address.Equal(remoteIP) {
			return true
		}
	}
	return false
}

func (g *Gateway) tryDirectMediaRequest(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	itemID, candidate := segmentedItemID(r.URL.Path)
	segmented := candidate
	if !candidate {
		itemID, candidate = streamItemID(r.URL.Path)
	}
	if !candidate {
		return false
	}
	token := jellyfinToken(r)
	if token == "" {
		return false
	}
	query := url.Values{"UserId": {queryFold(r.URL.Query(), "UserId")}}
	if query.Get("UserId") == "" {
		query.Del("UserId")
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/Items/" + url.PathEscape(itemID) + "/PlaybackInfo"
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	setAuthHeaders(req, token)
	client := g.BackendHTTP
	if client == nil {
		client = g.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "media source lookup failed", http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			status = resp.StatusCode
		}
		http.Error(w, "media source lookup failed", status)
		return true
	}
	var payload struct {
		MediaSources []struct {
			ID   string `json:"Id"`
			Path string `json:"Path"`
		} `json:"MediaSources"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload) != nil {
		http.Error(w, "invalid media source response", http.StatusBadGateway)
		return true
	}
	wanted := queryFold(r.URL.Query(), "MediaSourceId")
	var matches []struct{ id, sig string }
	var cmsMatches []CMSMediaSource
	bridge := g.cmsBridge(r.Context())
	hasDirectSource := false
	for _, source := range payload.MediaSources {
		if bridge != nil {
			if cms, ok := bridge.ParseSource(source.Path); ok && bridge.Eligible(r.Context(), cms.PickCode) {
				hasDirectSource = true
				if wanted == "" || source.ID == wanted {
					cmsMatches = append(cmsMatches, cms)
				}
				continue
			}
		}
		id, sig, ok := directIdentity(source.Path)
		if !ok {
			if wanted != "" && source.ID == wanted {
				return false
			}
			continue
		}
		hasDirectSource = true
		if wanted != "" && source.ID != wanted {
			continue
		}
		matches = append(matches, struct{ id, sig string }{id, sig})
	}
	if !hasDirectSource {
		return false
	}
	if len(cmsMatches) > 0 {
		if len(cmsMatches)+len(matches) != 1 {
			http.Error(w, "select a valid CMS media source", 400)
			return true
		}
		if segmented {
			http.Error(w, "CMS media requires direct playback", 409)
			return true
		}
		ext := cmsMatches[0].Extension
		if ext == "" {
			ext = "mkv"
		}
		copyRequest := r.Clone(r.Context())
		copyRequest.URL, _ = url.Parse(bridge.playPath(g.Secret, cmsMatches[0], ext, token))
		bridge.play(g, w, copyRequest)
		return true
	}
	if len(matches) != 1 {
		http.Error(w, "select a valid direct media source", http.StatusBadRequest)
		return true
	}
	if !organize.VerifyMedia(g.Secret, matches[0].id, matches[0].sig) {
		http.Error(w, "invalid media signature", http.StatusForbidden)
		return true
	}
	if segmented {
		if err := g.authorize(r.Context(), token); err != nil {
			http.Error(w, "Jellyfin authorization required", http.StatusUnauthorized)
			return true
		}
		g.recordDelivery(r, matches[0].id, "segmented_playback_blocked", http.StatusConflict, linkResult{}, false)
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{
			"code": "direct_play_required", "message": "115 媒体仅支持原片直链播放，请重新开始播放并使用支持原片格式的客户端",
		}})
		return true
	}
	g.redirectMedia(w, r, matches[0].id)
	return true
}

func streamItemID(path string) (string, bool) {
	parts := strings.Split(strings.Trim(mediaRoutePath(path), "/"), "/")
	if len(parts) == 3 && (strings.EqualFold(parts[0], "Videos") || strings.EqualFold(parts[0], "Audio")) && (strings.EqualFold(parts[2], "stream") || strings.HasPrefix(strings.ToLower(parts[2]), "stream.")) && !strings.HasSuffix(strings.ToLower(parts[2]), ".m3u8") {
		return parts[1], parts[1] != ""
	}
	if len(parts) == 3 && strings.EqualFold(parts[0], "Items") && (strings.EqualFold(parts[2], "Download") || strings.EqualFold(parts[2], "File")) {
		return parts[1], parts[1] != ""
	}
	if len(parts) == 3 && strings.EqualFold(parts[0], "Audio") && strings.EqualFold(parts[2], "universal") {
		return parts[1], parts[1] != ""
	}
	return "", false
}

func segmentedItemID(path string) (string, bool) {
	parts := strings.Split(strings.Trim(mediaRoutePath(path), "/"), "/")
	if len(parts) < 3 || (!strings.EqualFold(parts[0], "Videos") && !strings.EqualFold(parts[0], "Audio")) || parts[1] == "" {
		return "", false
	}
	// Playlists and their HLS/DASH fragments must never fall through to FFmpeg.
	for _, part := range parts[2:] {
		part = strings.ToLower(part)
		if strings.HasSuffix(part, ".m3u8") || strings.HasSuffix(part, ".mpd") ||
			strings.HasPrefix(part, "hls") || strings.HasPrefix(part, "dash") {
			return parts[1], true
		}
	}
	return "", false
}

func queryFold(values url.Values, key string) string {
	for current, list := range values {
		if strings.EqualFold(current, key) && len(list) > 0 {
			return list[0]
		}
	}
	return ""
}

func (g *Gateway) authorize(ctx context.Context, token string) error {
	if token == "" {
		return fmt.Errorf("missing token")
	}
	g.mu.Lock()
	if until := g.authCache[token]; time.Now().Before(until) {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()
	cfg, err := g.Config(ctx)
	if err != nil || cfg.URL == "" {
		return fmt.Errorf("Jellyfin is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.URL, "/")+"/Users/Me", nil)
	if err != nil {
		return err
	}
	setAuthHeaders(req, token)
	client := g.BackendHTTP
	if client == nil {
		client = g.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Jellyfin rejected token")
	}
	var user struct {
		Policy struct {
			EnableMediaPlayback bool `json:"EnableMediaPlayback"`
		} `json:"Policy"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user) != nil || !user.Policy.EnableMediaPlayback {
		return fmt.Errorf("playback is not allowed")
	}
	g.mu.Lock()
	g.authCache[token] = time.Now().Add(time.Minute)
	g.mu.Unlock()
	return nil
}

func jellyfinToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, key := range []string{"api_key", "X-Emby-Token", "X-MediaBrowser-Token"} {
		if token := queryFold(r.URL.Query(), key); token != "" {
			return token
		}
		if token := r.Header.Get(key); token != "" {
			return token
		}
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		auth = r.Header.Get("X-Emby-Authorization")
	}
	if index := strings.IndexByte(auth, ' '); index >= 0 {
		scheme := strings.TrimSpace(auth[:index])
		if strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(auth[index+1:])
		}
		if strings.EqualFold(scheme, "MediaBrowser") || strings.EqualFold(scheme, "Emby") {
			auth = auth[index+1:]
		}
	}
	for _, field := range strings.Split(auth, ",") {
		parts := strings.SplitN(strings.TrimSpace(field), "=", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "Token") {
			return strings.Trim(strings.TrimSpace(parts[1]), "\"")
		}
	}
	return ""
}

func validateLink(raw string, headers map[string]string, ua string, fallback time.Time) (time.Time, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(raw, "\r\n") {
		return time.Time{}, fmt.Errorf("invalid CDN URL")
	}
	if u.Query().Get("f") == "3" {
		return time.Time{}, fmt.Errorf("CDN URL requires cookie")
	}
	for key, value := range headers {
		if strings.EqualFold(key, "User-Agent") && value != ua {
			return time.Time{}, fmt.Errorf("CDN URL user agent mismatch")
		}
		if strings.EqualFold(key, "Cookie") && value != "" && u.Query().Get("f") != "1" && u.Query().Get("f") != "2" {
			return time.Time{}, fmt.Errorf("CDN URL requires cookie")
		}
	}
	if ts, err := strconv.ParseInt(u.Query().Get("t"), 10, 64); err == nil {
		deadline := time.Unix(ts, 0).Add(-5 * time.Minute)
		if deadline.Before(fallback) {
			fallback = deadline
		}
	}
	if !fallback.After(time.Now()) {
		return time.Time{}, errLinkExpired
	}
	return fallback, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
