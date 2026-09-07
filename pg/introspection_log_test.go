package pg

// specs/logging.md ## Introspection logging : introspect must emit one
// info-level summary line, plus debug-level per-symbol lines, every time
// it runs — not just once at process startup (TestMain already ran it once
// before this test even starts).

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestIntrospectionLog_SummaryAndPerSymbol(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	db, err := NewInfos(testDbURI)
	if err != nil {
		t.Fatalf("NewInfos: %v", err)
	}
	db.Pool.Close()

	var summary map[string]any
	var sawRelation, sawFunction bool
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decoding log line %q: %v", raw, err)
		}
		switch decoded["msg"] {
		case "introspection complete":
			summary = decoded
		case "relation found":
			sawRelation = true
		case "function found":
			sawFunction = true
		}
	}

	if summary == nil {
		t.Fatalf("expected an \"introspection complete\" summary line, got none in %q", buf.String())
	}
	for _, key := range []string{"relation_count", "function_count", "type_count", "anonymous_role_exists"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("introspection summary missing %q : %v", key, summary)
		}
	}
	if !sawRelation {
		t.Errorf("expected at least one debug \"relation found\" line")
	}
	if !sawFunction {
		t.Errorf("expected at least one debug \"function found\" line")
	}
}
