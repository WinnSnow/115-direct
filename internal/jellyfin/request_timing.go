package jellyfin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type playbackTimingKey struct{}

var playbackRequestSequence atomic.Uint64

// All request durations use Go's monotonic clock. In particular, no duration is
// inferred by subtracting timestamps from different HTTP requests.
type playbackTiming struct {
	id           string
	started      time.Time
	headersReady time.Time
	steps        map[string]time.Duration
	backendCalls int
	mediaID      string
	link         linkResult
}

func requestTiming(ctx context.Context) *playbackTiming {
	t, _ := ctx.Value(playbackTimingKey{}).(*playbackTiming)
	return t
}

func addRequestTiming(ctx context.Context, step string, started time.Time) {
	if t := requestTiming(ctx); t != nil {
		t.steps[step] += time.Since(started)
	}
}

func timedPlaybackPath(path string) bool {
	path = strings.ToLower(mediaRoutePath(path))
	if strings.HasPrefix(path, "/direct/") || strings.HasPrefix(path, "/cms/play/") || strings.Contains(path, "/playbackinfo") || strings.HasPrefix(path, "/sessions/playing") {
		return true
	}
	if _, ok := streamItemID(path); ok {
		return true
	}
	_, ok := segmentedItemID(path)
	return ok
}

func beginPlaybackTiming(w http.ResponseWriter, r *http.Request) (*timedPlaybackWriter, *http.Request) {
	started := time.Now()
	t := &playbackTiming{
		id:      strconv.FormatInt(started.UnixNano(), 36) + "-" + strconv.FormatUint(playbackRequestSequence.Add(1), 36),
		started: started,
		steps:   make(map[string]time.Duration),
	}
	w.Header().Set("X-115-Direct-Request-ID", t.id)
	return &timedPlaybackWriter{ResponseWriter: w, timing: t}, r.WithContext(context.WithValue(r.Context(), playbackTimingKey{}, t))
}

type timedPlaybackWriter struct {
	http.ResponseWriter
	timing *playbackTiming
	status int
}

func (w *timedPlaybackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *timedPlaybackWriter) WriteHeader(status int) {
	// Informational responses do not commit the final response.
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.timing.headersReady = time.Now()
	w.Header().Add("Server-Timing", fmt.Sprintf("gateway;dur=%.3f", milliseconds(w.timing.headersReady.Sub(w.timing.started))))
	w.ResponseWriter.WriteHeader(status)
}

func (w *timedPlaybackWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *timedPlaybackWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *timedPlaybackWriter) Flush() { _ = w.FlushError() }

func (w *timedPlaybackWriter) finish(r *http.Request) {
	finished := time.Now()
	t := w.timing
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	attrs := []any{
		"request_id", t.id, "method", r.Method, "path", mediaRoutePath(r.URL.Path),
		"media", t.mediaID, "client", clientName(r), "ua_hash", tokenHash(r.UserAgent())[:12],
		"backend_probe", isBackendProbe(r), "status", status,
		"request_started", t.started.UTC().Format(time.RFC3339Nano),
		"request_finished", finished.UTC().Format(time.RFC3339Nano),
		"elapsed_ms", milliseconds(finished.Sub(t.started)),
		"backend_calls", t.backendCalls, "cache", t.link.Cache, "attempts", t.link.Attempts,
		"context_canceled", r.Context().Err() != nil,
	}
	if !t.headersReady.IsZero() {
		attrs = append(attrs, "headers_ready_ms", milliseconds(t.headersReady.Sub(t.started)))
	}
	for _, step := range []string{"authorization", "media_lookup", "settings", "link_resolve", "backend_headers", "playback_info_rewrite"} {
		attrs = append(attrs, step+"_ms", milliseconds(t.steps[step]))
	}
	// Job timings describe the shared fetch as a whole, whereas link_resolve_ms
	// is this request's own wait. A late joiner must not be charged the full job.
	attrs = append(attrs,
		"link_job_queue_ms", milliseconds(t.link.QueueDuration),
		"link_job_115_ms", milliseconds(t.link.DownloadDuration),
		"link_job_cdn_ms", milliseconds(t.link.ProbeDuration),
	)
	slog.Info("gateway request", attrs...)
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

type playbackTimingTransport struct {
	base http.RoundTripper
}

func (t playbackTimingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	started := time.Now()
	resp, err := t.base.RoundTrip(r)
	if timing := requestTiming(r.Context()); timing != nil {
		timing.steps["backend_headers"] += time.Since(started)
		timing.backendCalls++
	}
	return resp, err
}

func timedBackendTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return playbackTimingTransport{base: base}
}

func timedBackendClient(client *http.Client, ctx context.Context) *http.Client {
	if requestTiming(ctx) == nil {
		return client
	}
	copyClient := *client
	copyClient.Transport = timedBackendTransport(client.Transport)
	return &copyClient
}
