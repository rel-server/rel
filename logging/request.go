package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

type requestLoggerKey struct{}

func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, requestLoggerKey{}, logger)
}

// FromContext returns the request logger RequestMiddleware stored, or slog.Default() ; never nil.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(requestLoggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

// RequestMiddleware reads X-Request-Id (or generates one) and stores on the context a logger that appends
// request_id to every line. The ID is not echoed back in the response.
func RequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newRequestID()
		}
		logger := slog.New(requestIDHandler{inner: slog.Default().Handler(), id: id})
		next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), logger)))
	})
}

// requestIDHandler appends request_id to each record rather than using With, which would print it first on
// every line. inner is slog.Default()'s handler as of the request's start.
type requestIDHandler struct {
	inner slog.Handler
	id    string
}

func (h requestIDHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone() // AddAttrs may write into storage shared with the caller's copy
	r.AddAttrs(slog.String("request_id", h.id))
	return h.inner.Handle(ctx, r)
}

func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{inner: h.inner.WithAttrs(attrs), id: h.id}
}

// WithGroup nests request_id into the group ; nothing in rel opens groups on a request logger.
func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{inner: h.inner.WithGroup(name), id: h.id}
}

// newRequestID is a short random hex token ; it only needs to be unique within the log retention window.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
