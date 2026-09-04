package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestMiddleware_GeneratesAndLogsRequestID : no inbound
// X-Request-Id still gets one generated, emitted as "request_id" in logs.
func TestRequestMiddleware_GeneratesAndLogsRequestID(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	var seenID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).Info("handling request")
		seenID = "captured"
	})
	handler := RequestMiddleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if seenID == "" {
		t.Fatal("expected the inner handler to run")
	}

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decoding log line: %v (raw: %s)", err, buf.String())
	}
	id, ok := line["request_id"].(string)
	if !ok || id == "" {
		t.Errorf("expected a non-empty request_id attribute, got %#v", line["request_id"])
	}
}

// TestRequestMiddleware_ReusesInboundRequestID : an inbound X-Request-Id
// header is reused verbatim, not always regenerated (## Request-scoped logging).
func TestRequestMiddleware_ReusesInboundRequestID(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).Info("handling request")
	})
	handler := RequestMiddleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "caller-supplied-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decoding log line: %v (raw: %s)", err, buf.String())
	}
	if got := line["request_id"]; got != "caller-supplied-id" {
		t.Errorf("expected request_id=caller-supplied-id, got %#v", got)
	}
}

// TestFromContext_FallsBackToDefault : a context that never passed through
// RequestMiddleware still gets a usable logger, not a nil one.
func TestFromContext_FallsBackToDefault(t *testing.T) {
	logger := FromContext(t.Context())
	if logger == nil {
		t.Fatal("expected a non-nil fallback logger")
	}
}
