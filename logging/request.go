// This file derives a per-request child logger tagged with a stable request_id and
// making it retrievable from context, so every log line emitted while
// handling one request can be correlated end to end.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// requestLoggerKey is unexported so no other package can collide by
// constructing an equal-by-value context key — same convention as jwt's contextKey.
type requestLoggerKey struct{}

// WithContext stashes logger on ctx, retrievable via FromContext.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, requestLoggerKey{}, logger)
}

// FromContext is how application code retrieves its logger, rather than
// calling slog.Default() directly : it returns the request-scoped logger RequestMiddleware stashed,
// or slog.Default() as a safe fallback for a ctx that never passed through
// it (startup code, background jobs, or a test calling application code
// directly with context.Background()) — same "always usable, never nil"
// posture as jwtpkg.FromContext's own (Claims, bool) fallback.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(requestLoggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

// RequestMiddleware does three things : read (or generate) a request ID, derive a child logger that
// appends request_id as the last attribute of every line, store it on the request's context. Deliberately
// does NOT echo the ID back as a response header — request_id's whole job
// here is correlating log lines server-side ; a response header is
// access logging's separate concern.
//
// slog.Default() is resolved fresh per request, not captured once at
// construction — same reasoning as dynamicHandler's (logging.go).
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

// requestIDHandler appends request_id to each record rather than using With, because attrs added
// through With are printed before the record's own, which would put request_id first on every line.
// The inner handler is the one slog.Default() had when the request began.
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

// WithGroup nests request_id into the group along with the record's other attrs ; nothing in rel
// opens groups on a request logger.
func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{inner: h.inner.WithGroup(name), id: h.id}
}

// newRequestID is a short random hex token ; 8 bytes is plenty since
// collision-safety only needs to hold within one process's log retention window.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
