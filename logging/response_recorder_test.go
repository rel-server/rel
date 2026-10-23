package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartAccessLog_LogsStatusSizeAndTheCallersAttrs(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec, done := StartAccessLog(httptest.NewRecorder(), r)
	role := "" // filled in after the log function is built, read when it fires
	rec.WriteHeader(http.StatusTeapot)
	_, _ = rec.Write([]byte("abc"))
	role = "member"
	done("role", role)

	line := buf.String()
	for _, want := range []string{"msg=request", "method=POST", "path=/x", "status=418", "response_size=3", "duration=", "role=member"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q", line, want)
		}
	}
}
