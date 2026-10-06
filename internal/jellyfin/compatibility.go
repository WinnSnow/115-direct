package jellyfin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type PlaybackOptions struct {
	PublicURL string `json:"public_url"`
	CheckLink bool   `json:"check_link"`
}

func (o PlaybackOptions) Validate() error {
	if o.PublicURL == "" {
		return nil
	}
	u, err := url.Parse(o.PublicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return fmt.Errorf("播放公网地址必须为 HTTP(S) 网关根地址，不含路径、凭据或查询参数")
	}
	return nil
}

func (g *Gateway) playbackOptions(ctx context.Context) (PlaybackOptions, error) {
	var o PlaybackOptions
	err := g.Store.GetSetting(ctx, "playback", &o)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err == nil {
		err = o.Validate()
	}
	return o, err
}

func (g *Gateway) viewerBase(r *http.Request) string {
	options, err := g.playbackOptions(r.Context())
	if err == nil && options.PublicURL != "" {
		return strings.TrimRight(options.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// A fixed public_url takes precedence when a tunnel replaces the Host header.
	if proto := strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]; proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func mediaRoutePath(path string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	if len(parts) == 2 && (strings.EqualFold(parts[0], "emby") || strings.EqualFold(parts[0], "jellyfin")) {
		return "/" + parts[1]
	}
	return path
}

func mediaExtension(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "mp4", "mkv", "avi", "mov", "webm", "m4v", "ts", "m2ts", "mp3", "flac", "m4a", "aac", "ogg", "wav":
		return ext
	}
	return ""
}

func clientName(r *http.Request) string {
	name := queryFold(r.URL.Query(), "X-Emby-Client")
	if name == "" {
		name = r.Header.Get("X-Emby-Client")
	}
	if name != "" {
		return cleanClient(name)
	}
	ua := strings.ToLower(r.UserAgent())
	for _, client := range []string{"infuse", "vidhub", "senplayer", "fileball", "mpv", "vlc", "potplayer", "jellyfinmediaplayer", "jellyfin android", "jellyfin"} {
		if strings.Contains(ua, client) {
			return client
		}
	}
	if strings.Contains(ua, "chrome") || strings.Contains(ua, "safari") || strings.Contains(ua, "firefox") {
		return "browser"
	}
	if strings.Contains(ua, "lavf") || strings.Contains(ua, "ffprobe") {
		return "backend_probe"
	}
	return "unknown"
}

func cleanClient(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < ' ' || r == 127 {
			return -1
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

func isBackendProbe(r *http.Request) bool {
	return jellyfinToken(r) == "" && r.URL.Query().Get("jellyfin_sig") != ""
}
