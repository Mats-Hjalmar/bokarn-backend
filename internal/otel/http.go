// Package otel wires up OpenTelemetry tracing, metrics, and logging.
package otel

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/felixge/httpsnoop"
	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/router"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

// unmatchedRoute is the low-cardinality label used when a request matches no
// registered route (404s, raw handlers). Never fall back to the raw path — that
// would explode metric and log cardinality.
const unmatchedRoute = "unmatched"

// Middleware returns HTTP middleware that traces requests under the given name.
// It stays outermost so the trace/span and the base http.server.* metrics wrap
// the whole request; the route label is attached from EnrichContext.
func Middleware(name string) func(http.Handler) http.Handler {
	return otelhttp.NewMiddleware(name)
}

// EnrichContext is the outermost router middleware. It attaches the per-request
// info holder, resolves the matched route template, and tags the otelhttp
// Labeler with it (so http.server.request.duration gains an http.route label
// — otelhttp cannot derive it here because auth swaps the request instance).
// After the handler completes it emits one access-log line carrying route and
// user,
// at error level for 5xx and warn for 4xx.
func EnrichContext(rt *router.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := unmatchedRoute
			if ep := rt.Match(r); ep != nil {
				route = ep.Method + " " + ep.Path
			}

			ctx, _ := withRequestInfo(r.Context())
			SetRoute(ctx, route)

			if labeler, ok := otelhttp.LabelerFromContext(ctx); ok {
				labeler.Add(attribute.String("http.route", route))
			}

			r = r.WithContext(ctx)
			m := httpsnoop.CaptureMetrics(next, w, r)

			level := slog.LevelInfo
			switch {
			case m.Code >= 500:
				level = slog.LevelError
			case m.Code >= 400:
				level = slog.LevelWarn
			}

			slog.LogAttrs(ctx, level, "http request",
				slog.String("subsystem", "http"),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", m.Code),
				slog.Int64("bytes", m.Written),
				slog.Duration("duration", m.Duration),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// Recover catches panics from downstream handlers, logs them as structured
// error events (carrying route/user/trace via the slog handler chain), counts
// them, and writes a 500 ProblemDetails. It replaces router.Recover() so panics
// reach the OTLP/Loki pipeline instead of stdlib log. Registered just inside
// EnrichContext so a caught panic still produces an access-log line.
func Recover() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok &&
					errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}

				ctx := r.Context()
				slog.LogAttrs(ctx, slog.LevelError, "panic recovered",
					slog.String("subsystem", "recover"),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				RecordPanic(ctx)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(
					router.InternalServerError("an unexpected error occurred"),
				)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// CaptureUser records the authenticated user id into the request-info holder.
// It must be registered immediately after the auth middleware so the principal
// is present; EnrichContext then reads it when logging the completed request.
func CaptureUser() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := auth.Principal[string](r.Context()); ok {
				SetUser(r.Context(), id)
			}
			next.ServeHTTP(w, r)
		})
	}
}
