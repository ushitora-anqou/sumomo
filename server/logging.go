package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// eventLog emits the wide events: one JSON object per request on stderr,
// next to the plain-text startup logs. JSON keeps every attribute of an
// event machine-readable so logs can be filtered and aggregated without
// parsing free-form lines.
var eventLog = slog.New(slog.NewJSONHandler(os.Stderr, nil))

// wideEvent accumulates the handler-side attributes of one request so that
// everything known about it — the wire basics recorded by accessLog plus
// the payload fields handlers attach via addEventAttrs — is emitted as a
// single wide event instead of several narrow log lines.
type wideEvent struct {
	attrs []slog.Attr
}

// wideEventKey is the context key carrying the current request's wideEvent.
type wideEventKey struct{}

// addEventAttrs attaches request-content attributes (e.g. the /translate
// payload) to the wide event of the request carried by ctx. It is a no-op
// when no event is in the context, e.g. unit tests calling routes()
// without the accessLog middleware.
func addEventAttrs(ctx context.Context, attrs ...slog.Attr) {
	if ev, ok := ctx.Value(wideEventKey{}).(*wideEvent); ok {
		ev.attrs = append(ev.attrs, attrs...)
	}
}

// statusRecorder wraps an http.ResponseWriter to capture the status code
// the handler wrote. It forwards Flush so streaming responses (jsonStream)
// keep working through the wrapper; without it the wrapped writer would
// not satisfy http.Flusher and streaming would panic on a nil flusher.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the wrapped writer when it supports flushing.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLog emits one wide event per request: a single JSON log line with
// the request basics (method, path, status, duration, remote address)
// merged with whatever request-content attributes the handlers attached.
// The query string is intentionally omitted and api_key values are never
// recorded (only whether one was supplied) so credentials never reach the
// log.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		ev := &wideEvent{}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), wideEventKey{}, ev)))
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := append([]slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			slog.String("remote_addr", r.RemoteAddr),
		}, ev.attrs...)
		eventLog.LogAttrs(context.Background(), slog.LevelInfo, "request", attrs...)
	})
}
