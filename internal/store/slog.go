package store

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type LogHandler struct {
	Store *Store
	Next  slog.Handler
	attrs []slog.Attr
	group string
}

func (h *LogHandler) Enabled(ctx context.Context, l slog.Level) bool { return l >= slog.LevelDebug }
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	appendAttr := func(a slog.Attr) { fmt.Fprintf(&b, " %s=%v", a.Key, a.Value) }
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool { appendAttr(a); return true })
	level := "info"
	if r.Level >= slog.LevelError {
		level = "error"
	} else if r.Level >= slog.LevelWarn {
		level = "warning"
	} else if r.Level < slog.LevelInfo {
		level = "debug"
	}
	h.Store.Log(ctx, "runtime", level, b.String())
	if h.Next != nil && h.Next.Enabled(ctx, r.Level) {
		clean := slog.NewRecord(r.Time, r.Level, Redact(b.String()), r.PC)
		return h.Next.Handle(ctx, clean)
	}
	return nil
}
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	copy.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &copy
}
func (h *LogHandler) WithGroup(group string) slog.Handler {
	copy := *h
	copy.group = group
	return &copy
}
