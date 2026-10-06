package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

var errLinkExpired = errors.New("CDN URL expired")

type linkResult struct {
	URL         string
	Cache       string
	Attempts    int
	ProbeStatus int
	Stage       string
}

type probeError struct{ status int }

func (e probeError) Error() string { return fmt.Sprintf("CDN probe HTTP %d", e.status) }
func (e probeError) stale() bool {
	return e.status == 401 || e.status == 403 || e.status == 404 || e.status == 410
}

func (g *Gateway) resolveLink(ctx context.Context, pickCode, ua string, check bool) (linkResult, error) {
	key := pickCode + "|" + ua
	g.mu.Lock()
	cached, ok := g.cache[key]
	if ok && time.Now().Before(cached.Expires) && (!check || time.Since(cached.Checked) < time.Minute) {
		g.mu.Unlock()
		status := 0
		if check {
			status = cached.ProbeStatus
		}
		return linkResult{URL: cached.URL, Cache: "hit", Stage: "redirect_ready", ProbeStatus: status}, nil
	}
	flightKey := key
	if check {
		flightKey += "|check"
	}
	f, exists := g.flights[flightKey]
	if !exists {
		f = &flight{done: make(chan struct{})}
		g.flights[flightKey] = f
		go func() {
			work, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			select {
			case g.linkGate <- struct{}{}:
				f.result, f.err = g.fetchLink(work, key, pickCode, ua, check)
				<-g.linkGate
			case <-work.Done():
				f.err = work.Err()
				f.result.Stage = "link_resolve"
			}
			g.mu.Lock()
			delete(g.flights, flightKey)
			close(f.done)
			g.mu.Unlock()
		}()
	}
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return linkResult{Stage: "link_resolve"}, ctx.Err()
	case <-f.done:
		result := f.result
		if exists {
			result.Cache = "shared"
		}
		return result, f.err
	}
}

func (g *Gateway) fetchLink(ctx context.Context, key, pickCode, ua string, check bool) (linkResult, error) {
	result := linkResult{Cache: "miss", Stage: "link_resolve"}
	g.mu.Lock()
	cached, ok := g.cache[key]
	g.mu.Unlock()
	if !ok || !time.Now().Before(cached.Expires) {
		cached = cachedLink{}
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			result.Cache = "refreshed"
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if cached.URL == "" {
			result.Stage = "link_resolve"
			result.Attempts++
			var direct string
			var headers map[string]string
			var err error
			if g.linkLookup != nil {
				direct, headers, err = g.linkLookup(ctx, pickCode, ua)
			} else {
				direct, headers, err = g.Pan.DownloadURL(ctx, pickCode, ua)
			}
			if err != nil {
				return result, errors.New("115 direct link request failed")
			}
			expires, err := validateLink(direct, headers, ua, time.Now().Add(25*time.Minute))
			if err != nil {
				lastErr = err
				if errors.Is(err, errLinkExpired) {
					continue
				}
				return result, err
			}
			cached = cachedLink{URL: direct, Expires: expires}
		} else {
			result.Cache = "hit"
		}
		result.URL = cached.URL
		if check && time.Since(cached.Checked) >= time.Minute {
			result.Stage = "cdn_probe"
			status, err := g.probeCDN(ctx, cached.URL, ua)
			result.ProbeStatus = status
			if err != nil {
				lastErr = err
				g.mu.Lock()
				delete(g.cache, key)
				g.mu.Unlock()
				cached = cachedLink{}
				var stale probeError
				if errors.As(err, &stale) && stale.stale() {
					continue
				}
				return result, err
			}
			cached.Checked = time.Now()
			cached.ProbeStatus = status
		}
		result.Stage = "redirect_ready"
		if check {
			result.ProbeStatus = cached.ProbeStatus
		}
		g.mu.Lock()
		if len(g.cache) > 4096 {
			g.cache = map[string]cachedLink{}
		}
		g.cache[key] = cached
		g.mu.Unlock()
		return result, nil
	}
	return result, lastErr
}

func (g *Gateway) probeCDN(ctx context.Context, location, ua string) (int, error) {
	work, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := *g.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(work, http.MethodGet, location, nil)
	if err != nil {
		return 0, errors.New("invalid CDN probe URL")
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("CDN probe connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return resp.StatusCode, probeError{resp.StatusCode}
	}
	if resp.Header.Get("Content-Type") != "" && !isMediaContentType(resp.Header.Get("Content-Type")) {
		return resp.StatusCode, errors.New("CDN returned a non-media response")
	}
	if n, err := io.CopyN(io.Discard, resp.Body, 1); n != 1 || err != nil {
		return resp.StatusCode, errors.New("CDN response has no media bytes")
	}
	return resp.StatusCode, nil
}

func isMediaContentType(value string) bool {
	value, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.HasPrefix(value, "video/") || strings.HasPrefix(value, "audio/") || value == "application/octet-stream" || value == "binary/octet-stream"
}
