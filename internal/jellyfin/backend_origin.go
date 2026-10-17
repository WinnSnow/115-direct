package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	errMissingJellyfinToken = errors.New("Jellyfin login required")
	errInvalidJellyfinToken = errors.New("Jellyfin token rejected")
	errPlaybackForbidden    = errors.New("Jellyfin playback permission denied")
	errBackendSignature     = errors.New("invalid Jellyfin backend signature")
	errBackendOrigin        = errors.New("Jellyfin backend source was not recognized")
	errBackendDiscovery     = errors.New("Jellyfin backend source verification unavailable")
)

type cachedBackendOrigin struct {
	key       string
	expires   time.Time
	addresses []net.IP
	err       error
}

// authorizeBackendOrigin is only called after verifying the media-specific
// backend signature. A published Docker address can differ from Jellyfin's
// outbound address; query the configured server itself to identify that address.
func (g *Gateway) authorizeBackendOrigin(ctx context.Context, remoteAddress string) error {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return errBackendOrigin
	}
	remoteIP := net.ParseIP(strings.SplitN(host, "%", 2)[0])
	if remoteIP == nil {
		return errBackendOrigin
	}
	cfg, err := g.Config(ctx)
	if err != nil {
		return errBackendDiscovery
	}
	target, err := url.Parse(cfg.URL)
	if err != nil || target.Hostname() == "" {
		return errBackendDiscovery
	}
	if targetIP := net.ParseIP(target.Hostname()); targetIP != nil {
		if targetIP.Equal(remoteIP) {
			return nil
		}
	} else if addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", target.Hostname()); err == nil {
		for _, address := range addresses {
			if address.Equal(remoteIP) {
				return nil
			}
		}
	}
	if cfg.APIKey == "" {
		return errBackendOrigin
	}
	addresses, err := g.backendAddresses(ctx, cfg)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if address.Equal(remoteIP) {
			return nil
		}
	}
	return errBackendOrigin
}

func (g *Gateway) backendAddresses(ctx context.Context, cfg Config) ([]net.IP, error) {
	// Separate from the link and playback-event lock so a discovery request does
	// not block unrelated playback. Serialize concurrent probes and cache results.
	g.backendMu.Lock()
	defer g.backendMu.Unlock()
	key := cfg.URL + "|" + tokenHash(cfg.APIKey)
	if g.backendOrigin.key == key && time.Now().Before(g.backendOrigin.expires) {
		return g.backendOrigin.addresses, g.backendOrigin.err
	}
	addresses, err := g.discoverBackendAddresses(ctx, cfg)
	ttl := time.Minute
	if err != nil {
		ttl = 5 * time.Second
	}
	if ctx.Err() == nil {
		g.backendOrigin = cachedBackendOrigin{key: key, expires: time.Now().Add(ttl), addresses: addresses, err: err}
	}
	return addresses, err
}

func (g *Gateway) discoverBackendAddresses(ctx context.Context, cfg Config) ([]net.IP, error) {
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(work, http.MethodGet, strings.TrimRight(cfg.URL, "/")+"/System/Info", nil)
	if err != nil {
		return nil, errBackendDiscovery
	}
	setAuthHeaders(req, cfg.APIKey)
	client := g.BackendHTTP
	if client == nil {
		client = g.HTTP
	}
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := timedBackendClient(&requestClient, work).Do(req)
	if err != nil {
		return nil, errBackendDiscovery
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errBackendDiscovery
	}
	var info struct {
		LocalAddress   string
		LocalAddresses []string
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info) != nil {
		return nil, errBackendDiscovery
	}
	var addresses []net.IP
	for _, raw := range append(info.LocalAddresses, info.LocalAddress) {
		address, err := url.Parse(raw)
		if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.User != nil {
			continue
		}
		if ip := net.ParseIP(strings.SplitN(address.Hostname(), "%", 2)[0]); ip != nil {
			addresses = append(addresses, ip)
		}
	}
	return addresses, nil
}

func directAuthorizationFailure(err error) (int, string, string) {
	switch {
	case errors.Is(err, errMissingJellyfinToken):
		return http.StatusUnauthorized, "login_required", errMissingJellyfinToken.Error()
	case errors.Is(err, errInvalidJellyfinToken):
		return http.StatusUnauthorized, "token_rejected", errInvalidJellyfinToken.Error()
	case errors.Is(err, errPlaybackForbidden):
		return http.StatusForbidden, "playback_forbidden", errPlaybackForbidden.Error()
	case errors.Is(err, errBackendSignature):
		return http.StatusUnauthorized, "backend_signature_invalid", errBackendSignature.Error()
	case errors.Is(err, errBackendOrigin):
		return http.StatusForbidden, "backend_origin_mismatch", errBackendOrigin.Error()
	case errors.Is(err, errBackendDiscovery):
		return http.StatusBadGateway, "backend_origin_unavailable", errBackendDiscovery.Error()
	default:
		return http.StatusBadGateway, "user_authorization_unavailable", "Jellyfin user authorization unavailable"
	}
}
