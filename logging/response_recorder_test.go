package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLog_LogsStatusSizeAndTheHandlersAnnotations(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer AnnotateAccess(r.Context(), "member", "extra", 1)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("abc"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", nil))

	line := buf.String()
	for _, want := range []string{"extra=1", "duration="} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q", line, want)
		}
	}
	// path, method and role lead the line, in that order
	if !strings.Contains(line, "msg=request path=/x method=POST role=member status=418 response_size=3 ") {
		t.Errorf("unexpected access line %q", line)
	}
}

// TestAccessLog_HandlerThatNeverAnnotates : a request no handler annotated (a static file, a
// rejection) still gets its line, without a role.
func TestAccessLog_HandlerThatNeverAnnotates(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	AccessLog(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing.css", nil))

	line := buf.String()
	if !strings.Contains(line, "msg=request path=/missing.css method=GET status=404 ") || strings.Contains(line, "role=") {
		t.Errorf("unexpected access line %q", line)
	}
}

// TestAnnotateAccess_WithoutAccessLogIsANoop : a context that never passed through AccessLog is fine.
func TestAnnotateAccess_WithoutAccessLogIsANoop(t *testing.T) {
	AnnotateAccess(t.Context(), "r", "k", 1)
}
