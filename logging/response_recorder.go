package logging

import (
	"net/http"
	"time"
)

// ResponseRecorder wraps an http.ResponseWriter to capture the status code
// and byte count actually written, for specs/logging.md ## Access logging —
// callers keep writing through it exactly as they would the original
// http.ResponseWriter ; no downstream response helper needs to change.
type ResponseRecorder struct {
	http.ResponseWriter
	Status int
	Size   int
}

// NewResponseRecorder wraps w. Status defaults to 200, matching
// net/http's own behavior when a handler never calls WriteHeader.
func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{ResponseWriter: w, Status: http.StatusOK}
}

func (r *ResponseRecorder) WriteHeader(status int) {
	r.Status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *ResponseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.Size += n
	return n, err
}

// StartAccessLog wraps w in a ResponseRecorder and returns it with the function that writes the
// request's one access-log line (specs/logging.md ## Access logging) : status, size and duration
// are added to the method and path, then attrs. Call it from a defer so it fires from every
// return path, an early rejection included ; a closure over variables filled in later reads their
// final values.
func StartAccessLog(w http.ResponseWriter, r *http.Request) (*ResponseRecorder, func(attrs ...any)) {
	rec := NewResponseRecorder(w)
	start := time.Now()
	return rec, func(attrs ...any) {
		FromContext(r.Context()).Info("request", append([]any{
			"method", r.Method, "path", r.URL.Path,
			"status", rec.Status, "response_size", rec.Size, "duration", time.Since(start),
		}, attrs...)...)
	}
}
