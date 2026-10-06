package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/local/115-direct/internal/organize"
)

type PlaybackEvent struct {
	Time          time.Time `json:"time"`
	MediaID       string    `json:"media_id"`
	Client        string    `json:"client"`
	Entry         string    `json:"entry"`
	Mode          string    `json:"mode"`
	Status        int       `json:"status,omitempty"`
	CDNHost       string    `json:"cdn_host,omitempty"`
	Cache         string    `json:"cache,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	ProbeStatus   int       `json:"probe_status,omitempty"`
	Stage         string    `json:"stage,omitempty"`
	PositionTicks int64     `json:"position_ticks,omitempty"`
	PlayMethod    string    `json:"play_method,omitempty"`
}

type Diagnostic struct {
	MediaID                string          `json:"media_id"`
	Name                   string          `json:"name"`
	Container              string          `json:"container"`
	Client                 string          `json:"client"`
	Stage                  string          `json:"stage"`
	RedirectReady          bool            `json:"redirect_ready"`
	CDNHost                string          `json:"cdn_host,omitempty"`
	Cache                  string          `json:"cache,omitempty"`
	Attempts               int             `json:"attempts"`
	ProbeStatus            int             `json:"probe_status,omitempty"`
	Error                  string          `json:"error,omitempty"`
	ClientPlaybackVerified bool            `json:"client_playback_verified"`
	Events                 []PlaybackEvent `json:"events"`
}

func (g *Gateway) MediaID(raw string) (string, bool) {
	id, sig, ok := directIdentity(raw)
	return id, ok && organize.VerifyMedia(g.Secret, id, sig)
}

func (g *Gateway) Diagnose(ctx context.Context, id, ua string, check bool) Diagnostic {
	d := Diagnostic{MediaID: id, Stage: "media_lookup", Events: g.PlaybackEvents(id)}
	media, err := g.Store.GetMedia(ctx, id)
	if err != nil {
		d.Error = "媒体台账中未找到此文件"
		return d
	}
	d.Name, d.Container = media.Name, mediaExtension(media.Name)
	req, _ := http.NewRequest(http.MethodHead, "http://gateway/direct/"+url.PathEscape(id), nil)
	req.Header.Set("User-Agent", ua)
	d.Client = clientName(req)
	result, err := g.resolveLink(ctx, media.PickCode, ua, check)
	d.Stage, d.Cache, d.Attempts, d.ProbeStatus = result.Stage, result.Cache, result.Attempts, result.ProbeStatus
	if cdn, err := url.Parse(result.URL); err == nil {
		d.CDNHost = cdn.Hostname()
	}
	if err != nil {
		d.Error = err.Error()
	} else {
		d.RedirectReady = true
	}
	if !check {
		d.ProbeStatus = 0
	}
	g.Store.Audit(ctx, "playback", fmt.Sprintf("单文件诊断 media=%s stage=%s cdn=%s status=%d", id, d.Stage, d.CDNHost, d.ProbeStatus))
	return d
}

func (g *Gateway) PlaybackEvents(id string) []PlaybackEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	items := make([]PlaybackEvent, 0)
	for i := len(g.events) - 1; i >= 0; i-- {
		if id == "" || g.events[i].MediaID == id {
			items = append(items, g.events[i])
		}
	}
	return items
}

func (g *Gateway) recordDelivery(r *http.Request, id, mode string, status int, result linkResult, probe bool) {
	cdn, _ := url.Parse(result.URL)
	host := ""
	if cdn != nil {
		host = cdn.Hostname()
	}
	slog.Info("media delivery", "media", id, "mode", mode, "cdn_host", host, "client", clientName(r), "status", status, "cache", result.Cache, "attempts", result.Attempts, "stage", result.Stage, "backend_probe", probe)
	if probe {
		return
	}
	e := PlaybackEvent{Time: time.Now().UTC(), MediaID: id, Client: clientName(r), Entry: mediaRoutePath(r.URL.Path), Mode: mode, Status: status, CDNHost: host, Cache: result.Cache, Attempts: result.Attempts, ProbeStatus: result.ProbeStatus, Stage: result.Stage}
	g.addEvent(e)
	g.Store.Audit(r.Context(), "playback", fmt.Sprintf("%s media=%s client=%s status=%d cdn=%s stage=%s", mode, id, e.Client, status, host, result.Stage))
}

func (g *Gateway) addEvent(e PlaybackEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) >= 200 {
		g.events = append(g.events[:0], g.events[1:]...)
	}
	g.events = append(g.events, e)
}

func (g *Gateway) rememberSource(path string, source map[string]any, id string) {
	parts := strings.Split(strings.Trim(mediaRoutePath(path), "/"), "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "Items") {
		return
	}
	sourceID, _ := source["Id"].(string)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sources) > 4096 {
		g.sources = map[string]string{}
	}
	g.sources[normalizeItemID(parts[1])+"|"+sourceID] = id
}

func normalizeItemID(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }

type replayedBody struct {
	io.Reader
	io.Closer
}

func (g *Gateway) observePlayback(r *http.Request) {
	path := strings.ToLower(mediaRoutePath(r.URL.Path))
	if r.Method != http.MethodPost || (path != "/sessions/playing" && path != "/sessions/playing/progress" && path != "/sessions/playing/stopped") || r.Body == nil {
		return
	}
	if r.ContentLength > 1<<20 {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	// Preserve the request byte-for-byte, even if a client sends malformed data.
	r.Body = &replayedBody{Reader: io.MultiReader(bytes.NewReader(raw), r.Body), Closer: r.Body}
	if err != nil || len(raw) > 1<<20 {
		return
	}
	var event struct {
		ItemID        string `json:"ItemId"`
		MediaSourceID string `json:"MediaSourceId"`
		PositionTicks int64
		PlayMethod    string
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	g.mu.Lock()
	id := g.sources[normalizeItemID(event.ItemID)+"|"+event.MediaSourceID]
	g.mu.Unlock()
	if id == "" || g.authorize(r.Context(), jellyfinToken(r)) != nil {
		return
	}
	mode := "client_started"
	if strings.HasSuffix(path, "/progress") {
		mode = "client_progress"
	}
	if strings.HasSuffix(path, "/stopped") {
		mode = "client_stopped"
	}
	g.addEvent(PlaybackEvent{Time: time.Now().UTC(), MediaID: id, Client: clientName(r), Entry: path, Mode: mode, PositionTicks: event.PositionTicks, PlayMethod: cleanClient(event.PlayMethod), Stage: "client_report"})
}
