package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
)

// TestWriteError_LogsServerErrorInFull proves specs/logging.md ## Error
// logging : a 5xx is logged in full server-side even when dev=false hides
// the detail from the client itself (the exact gap found diagnosing the
// upsert identity bug — the client response and the server log are
// independent of each other).
func TestWriteError_LogsServerErrorInFull(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	cfg := config.Test() // Pg.Query.AnonymousRole intentionally unset
	cfg.Dev = false
	handler := NewRelHandler(testDb, cfg, nil)

	rec := postRelWithCookie(t, handler, `{
		"relation": "secret_notes", "schema": "public", "select": ["own"]
	}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "" || strings.Contains(rec.Body.String(), "no role configured") {
		t.Fatalf("expected the response body to stay redacted (dev=false), got %s", rec.Body.String())
	}

	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON log line: %v: %s", err, line)
		}
		if entry["msg"] != "request failed" {
			continue
		}
		found = true
		if entry["level"] != "ERROR" {
			t.Errorf("expected level=ERROR, got %v", entry["level"])
		}
		if entry["code"] != string(errcode.NoRoleConfigured) {
			t.Errorf("expected code=%s, got %v", errcode.NoRoleConfigured, entry["code"])
		}
		errText, _ := entry["error"].(string)
		if !strings.Contains(errText, "no role configured") {
			t.Errorf("expected the log line's error field to carry the full detail hidden from the client, got %q", errText)
		}
	}
	if !found {
		t.Fatalf("expected a \"request failed\" log line, got:\n%s", buf.String())
	}
}
