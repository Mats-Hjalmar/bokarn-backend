package otel

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"
)

func init() {
	_ = godotenv.Load()

	level := parseLevel(os.Getenv("LOG_LEVEL"))
	format := strings.ToLower(os.Getenv("LOG_FORMAT"))

	opts := &slog.HandlerOptions{Level: level}

	var base slog.Handler
	if format == "text" {
		base = slog.NewTextHandler(os.Stdout, opts)
	} else {
		base = slog.NewJSONHandler(os.Stdout, opts)
	}

	multi := &multiHandler{handlers: []slog.Handler{
		&traceContextHandler{handler: base},
		otelslog.NewHandler("bokarn-backend"),
	}}

	slog.SetDefault(slog.New(&requestContextHandler{handler: multi}))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type traceContextHandler struct {
	handler slog.Handler
}

func (h *traceContextHandler) Enabled(
	ctx context.Context,
	level slog.Level,
) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		r.AddAttrs(
			slog.String("trace_id", span.SpanContext().TraceID().String()),
			slog.String("span_id", span.SpanContext().SpanID().String()),
		)
	}
	return h.handler.Handle(ctx, r)
}

func (h *traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceContextHandler{handler: h.handler.WithAttrs(attrs)}
}

func (h *traceContextHandler) WithGroup(name string) slog.Handler {
	return &traceContextHandler{handler: h.handler.WithGroup(name)}
}

// requestContextHandler adds the matched route and authenticated user id to
// every record, so panics, 5xx responses, and any explicitly logged error carry
// the request context needed to cross-reference traces. It wraps the whole
// fan-out handler so both the stdout and OTLP branches receive the attributes.
// Empty values are omitted to avoid low-value empty-string labels in Loki.
type requestContextHandler struct {
	handler slog.Handler
}

func (h *requestContextHandler) Enabled(
	ctx context.Context,
	level slog.Level,
) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *requestContextHandler) Handle(
	ctx context.Context,
	r slog.Record,
) error {
	route, userID := RouteAndUser(ctx)
	if route != "" {
		r.AddAttrs(slog.String("http.route", route))
	}
	if userID != "" {
		r.AddAttrs(slog.String("user.id", userID))
	}
	return h.handler.Handle(ctx, r)
}

func (h *requestContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &requestContextHandler{handler: h.handler.WithAttrs(attrs)}
}

func (h *requestContextHandler) WithGroup(name string) slog.Handler {
	return &requestContextHandler{handler: h.handler.WithGroup(name)}
}

// multiHandler dispatches each record to every wrapped handler, so a single
// slog call can write to stdout and export over OTLP at once.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}
