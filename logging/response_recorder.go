package logging

import (
	"context"
	"io"
	"net/http"
	"time"
)

// ResponseRecorder captures the status code and byte count written, for the access log.
type ResponseRecorder struct {
	http.ResponseWriter
	Status int
	Size   int
}

// NewResponseRecorder wraps w. Status defaults to 200, for a handler that never calls WriteHeader.
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

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *ResponseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush flushes the underlying writer when it can.
func (r *ResponseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom keeps the underlying writer's own io.ReaderFrom fast path (file serving relies on it)
// while still counting the bytes.
func (r *ResponseRecorder) ReadFrom(src io.Reader) (int64, error) {
	var n int64
	var err error
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(struct{ io.Writer }{r.ResponseWriter}, src)
	}
	r.Size += int(n)
	return n, err
}

// accessKey is the context key of a request's accessInfo.
type accessKey struct{}

// accessInfo is what a handler adds to its request's access line.
type accessInfo struct {
	role  string
	attrs []any
}

// AnnotateAccess adds the request's resolved role, when it has one, and extra attributes to the
// access line AccessLog writes for it. Call it from a defer so it also runs on an early return.
// It does nothing when ctx did not pass through AccessLog.
func AnnotateAccess(ctx context.Context, role string, attrs ...any) {
	if info, ok := ctx.Value(accessKey{}).(*accessInfo); ok {
		info.role = role
		info.attrs = attrs
	}
}

// AccessLog writes one line per request once next returns, with what AnnotateAccess added. It must sit inside
// RequestMiddleware.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := NewResponseRecorder(w)
		start := time.Now()
		info := &accessInfo{}
		r = r.WithContext(context.WithValue(r.Context(), accessKey{}, info))
		defer func() {
			attrs := []any{"path", r.URL.Path, "method", r.Method}
			if info.role != "" {
				attrs = append(attrs, "role", info.role)
			}
			attrs = append(attrs, "status", rec.Status, "response_size", rec.Size, "duration", time.Since(start))
			FromContext(r.Context()).Info(accessLogMessage, append(attrs, info.attrs...)...)
		}()
		next.ServeHTTP(rec, r)
	})
}
