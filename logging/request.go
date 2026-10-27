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

// RequestMiddleware does three things : read (or generate) a request ID, derive a child logger via
// .With("request_id", id), store it on the request's context. Deliberately
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
		logger := slog.Default().With("request_id", id)
		next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), logger)))
	})
}

// newRequestID is a short random hex token ; 8 bytes is plenty since
// collision-safety only needs to hold within one process's log retention window.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
