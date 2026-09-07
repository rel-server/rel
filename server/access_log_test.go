package server

// specs/logging.md ## Access logging : handleRel must emit exactly one
// info-level "request" line per HTTP request, with item_count/write set
// from the resolved Sequence, plus debug-level per-query lines carrying
// the actual SQL sent to Postgres.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

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

func TestAccessLog_ReadRequest(t *testing.T) {
	var rec = struct{ Code int }{}
	lines := captureLogsAt(t, slog.LevelDebug, func() {
		r := postRel(t, `{"relation": "director", "limit": 1}`)
		rec.Code = r.Code
	})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	line := findLogLine(lines, "request")
	if line == nil {
		t.Fatalf("expected a \"request\" access line, got %v", lines)
	}
	for _, key := range []string{"method", "path", "status", "response_size", "duration", "verified", "role", "claims", "item_count", "write"} {
		if _, ok := line[key]; !ok {
			t.Errorf("access log line missing %q : %v", key, line)
		}
	}
	if line["item_count"] != float64(1) {
		t.Errorf("expected item_count=1, got %v", line["item_count"])
	}
	if line["write"] != false {
		t.Errorf("expected write=false for a bare read, got %v", line["write"])
	}

	read := findLogLine(lines, "executing read")
	if read == nil {
		t.Fatalf("expected a debug \"executing read\" line, got %v", lines)
	}
	if sql, _ := read["sql"].(string); !strings.Contains(sql, "director") {
		t.Errorf("expected the compiled sql to reference director, got %q", sql)
	}
}

func TestAccessLog_WriteRequestSetsWriteFlag(t *testing.T) {
	lines := captureLogsAt(t, slog.LevelDebug, func() {
		r := postRel(t, `{"query": {"relation": "director"}, "data": [{"name": "Access Log Test Director"}]}`)
		if r.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", r.Code, r.Body.String())
		}
	})

	line := findLogLine(lines, "request")
	if line == nil {
		t.Fatalf("expected a \"request\" access line, got %v", lines)
	}
	if line["write"] != true {
		t.Errorf("expected write=true for a write item, got %v", line["write"])
	}

	if findLogLine(lines, "executing write") == nil {
		t.Errorf("expected a debug \"executing write\" line, got %v", lines)
	}
}
