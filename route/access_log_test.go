package route

// Route access logging : the "request" line carries the route's own fields, and a debug "invoking route"
// line carries the compiled SQL.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLogsAt swaps slog's default handler to a JSON handler writing to a
// fresh buffer at the given level for the duration of fn, returning every
// decoded log line fn caused — logging.For(...)'s dynamicHandler resolves
// slog.Default() fresh on each call (logging.go), so this swap is visible
// to every package-level `log` var without any of them needing to change.
func captureLogsAt(t *testing.T, level slog.Level, fn func()) []map[string]any {
	t.Helper()
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))

	fn()

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decoding log line %q: %v", raw, err)
		}
		lines = append(lines, decoded)
	}
	return lines
}

func findLogLine(lines []map[string]any, msg string) map[string]any {
	for _, l := range lines {
		if l["msg"] == msg {
			return l
		}
	}
	return nil
}

func TestAccessLog_CarriesRouteFields(t *testing.T) {
	lines := captureLogsAt(t, slog.LevelDebug, func() {
		req := httptest.NewRequest(http.MethodGet, "/new/echo0", nil)
		rec := httptest.NewRecorder()
		testHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	line := findLogLine(lines, "request")
	if line == nil {
		t.Fatalf("expected a \"request\" access line, got %v", lines)
	}
	for _, key := range []string{"role", "function", "full_control"} {
		if _, ok := line[key]; !ok {
			t.Errorf("access log line missing %q : %v", key, line)
		}
	}

	invoke := findLogLine(lines, "invoking route")
	if invoke == nil {
		t.Fatalf("expected a debug \"invoking route\" line, got %v", lines)
	}
	if _, ok := invoke["sql"]; !ok {
		t.Errorf("expected \"invoking route\" line to carry the compiled sql, got %v", invoke)
	}
}

func TestRouteRegistryLog_SummaryAndPerRoute(t *testing.T) {
	var summary map[string]any
	var sawRoute bool
	lines := captureLogsAt(t, slog.LevelDebug, func() {
		if _, err := BuildRegistry(testDb, testCfg); err != nil {
			t.Fatalf("BuildRegistry: %v", err)
		}
	})
	for _, l := range lines {
		switch l["msg"] {
		case "route registry built":
			summary = l
		case "route registered":
			if l["function"] == "public.fn_new_echo0" {
				sawRoute = true
			}
		}
	}
	if summary == nil {
		t.Fatalf("expected a \"route registry built\" summary line, got %v", lines)
	}
	if _, ok := summary["route_count"]; !ok {
		t.Errorf("summary line missing route_count : %v", summary)
	}
	if !sawRoute {
		t.Errorf("expected a debug \"route registered\" line for fn_new_echo0, got %v", lines)
	}
}
