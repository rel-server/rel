package logging

import "net/http"

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
