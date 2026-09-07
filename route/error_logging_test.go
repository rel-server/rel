package route

// specs/logging.md ## Error logging : every 5xx route/response.go writes
// must be logged in full server-side, independent of what the client
// response itself shows.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rel-server/rel/errcode"
)

func TestWriteServerError_LogsInFull(t *testing.T) {
	underlying := errors.New("boom: acquiring connection failed")

	lines := captureLogsAt(t, slog.LevelDebug, func() {
		rec := httptest.NewRecorder()
		writeServerError(context.Background(), rec, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection", underlying)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		if got := rec.Body.String(); got != "acquiring connection" {
			t.Fatalf("expected the client-facing message unchanged, got %q", got)
		}
	})

	line := findLogLine(lines, "request failed")
	if line == nil {
		t.Fatalf("expected a \"request failed\" log line, got %#v", lines)
	}
	if line["code"] != string(errcode.DBUnavailable) {
		t.Errorf("expected code=%s, got %v", errcode.DBUnavailable, line["code"])
	}
	errText, _ := line["error"].(string)
	if errText != underlying.Error() {
		t.Errorf("expected the full underlying error logged, got %q", errText)
	}
}

func TestWriteErrorForPgErr_UnclassifiedLogsInFull(t *testing.T) {
	underlying := errors.New("some opaque failure never classified as a pg error")

	lines := captureLogsAt(t, slog.LevelDebug, func() {
		rec := httptest.NewRecorder()
		writeErrorForPgErr(context.Background(), rec, underlying, false)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		if got := rec.Body.String(); got != "internal error" {
			t.Fatalf("expected the redacted client-facing message, got %q", got)
		}
	})

	line := findLogLine(lines, "request failed")
	if line == nil {
		t.Fatalf("expected a \"request failed\" log line, got %#v", lines)
	}
	errText, _ := line["error"].(string)
	if errText != underlying.Error() {
		t.Errorf("expected the full underlying error logged despite the redacted response, got %q", errText)
	}
}
