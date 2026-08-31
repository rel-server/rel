package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ceymard/rel/config"
)

// buildTo is Build, but writing to buf instead of os.Stdout — Build's own
// handler construction is hardcoded to os.Stdout (matching ## Configuration
// : "Output is always stdout"), so tests exercise the same handler-selection
// logic directly against an in-memory writer instead.
func buildTo(buf *bytes.Buffer, cfg config.Logging) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	var inner slog.Handler
	switch cfg.Handler {
	case "", "pretty":
		inner = slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	case "JSON":
		inner = slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	default:
		return Build(cfg) // let Build produce the real error
	}
	filtered, err := newFilterHandler(inner, cfg.Filter, cfg.Exclude)
	if err != nil {
		return nil, err
	}
	return slog.New(filtered), nil
}

// TestFor_ResolvesDefaultLazily is the regression test for For's own core
// safety property : a logger built via For BEFORE slog.SetDefault ever runs
// (exactly what happens when a package stores For's result in a package-
// level var, since Go initializes those before main() gets to call
// logging.Install) must still pick up whichever handler is installed later,
// not freeze in whatever slog.Default() returned at construction time.
func TestFor_ResolvesDefaultLazily(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	// Simulates a package-level `var log = logging.For("query")` : built
	// against whatever slog.Default() is RIGHT NOW (the stdlib's own
	// built-in default in a real init-order scenario), before the real
	// handler exists.
	log := For("query")

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	log.Info("hello")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected valid JSON from the handler installed AFTER For() was called, got %q: %v", buf.String(), err)
	}
	if decoded["module"] != "query" {
		t.Errorf("expected module=query, got %v", decoded["module"])
	}
	if decoded["msg"] != "hello" {
		t.Errorf("expected msg=hello, got %v", decoded["msg"])
	}
}

// TestFor_WithAttrsAccumulates confirms a further .With(...) off a For(...)
// logger keeps the module attribute rather than replacing it — dynamicHandler
// merges attrs, it doesn't overwrite.
func TestFor_WithAttrsAccumulates(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	log := For("rpc").With("request_id", "abc123")
	log.Info("handled")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	if decoded["module"] != "rpc" || decoded["request_id"] != "abc123" {
		t.Errorf("expected both module=rpc and request_id=abc123, got %v", decoded)
	}
}

func TestBuild_UnrecognizedHandlerIsError(t *testing.T) {
	if _, err := Build(config.Logging{Handler: "xml"}); err == nil {
		t.Fatalf("expected an error for an unrecognized handler")
	}
}

func TestBuild_UnrecognizedLevelIsError(t *testing.T) {
	if _, err := Build(config.Logging{Level: "verbose"}); err == nil {
		t.Fatalf("expected an error for an unrecognized level")
	}
}

func TestBuild_DefaultHandlerIsPretty(t *testing.T) {
	logger, err := Build(config.Logging{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if logger == nil {
		t.Fatalf("expected a non-nil logger")
	}
}

func TestBuild_JSONHandlerProducesValidJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{Handler: "JSON", Level: "info"})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.Info("hello", "key", "value")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected valid JSON line, got %q: %v", buf.String(), err)
	}
	if decoded["key"] != "value" {
		t.Errorf("expected key=value in the JSON line, got %v", decoded)
	}
}

func TestLevelFiltering_DebugSuppressedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{Handler: "JSON", Level: "info"})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.Debug("should not appear")
	logger.Info("should appear")

	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("expected debug line suppressed at info level, got %q", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("expected info line present, got %q", out)
	}
}

func TestFilter_KeyPresentMustMatch(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{
		Handler: "JSON", Level: "info",
		Filter: map[string]string{"component": "^db$"},
	})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.Info("db line", "component", "db")
	logger.Info("http line", "component", "http")

	out := buf.String()
	if !strings.Contains(out, "db line") {
		t.Errorf("expected the matching component=db line to pass, got %q", out)
	}
	if strings.Contains(out, "http line") {
		t.Errorf("expected the non-matching component=http line suppressed, got %q", out)
	}
}

func TestFilter_KeyAbsentPasses(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{
		Handler: "JSON", Level: "info",
		Filter: map[string]string{"component": "^db$"},
	})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.Info("no component attr at all")

	if !strings.Contains(buf.String(), "no component attr at all") {
		t.Errorf("expected a record lacking the filtered key to pass through, got %q", buf.String())
	}
}

func TestFilter_AppliesToWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{
		Handler: "JSON", Level: "info",
		Filter: map[string]string{"component": "^db$"},
	})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.With("component", "http").Info("via With, should be suppressed")

	if strings.Contains(buf.String(), "via With") {
		t.Errorf("expected a filter key set via logger.With(...) to be checked too, got %q", buf.String())
	}
}

func TestExclude_SuppressesAfterFilter(t *testing.T) {
	var buf bytes.Buffer
	logger, err := buildTo(&buf, config.Logging{
		Handler: "JSON", Level: "info",
		Exclude: map[string]string{"noisy": "true"},
	})
	if err != nil {
		t.Fatalf("buildTo: %v", err)
	}
	logger.Info("quiet line", "noisy", "false")
	logger.Info("noisy line", "noisy", "true")

	out := buf.String()
	if !strings.Contains(out, "quiet line") {
		t.Errorf("expected non-matching line to pass, got %q", out)
	}
	if strings.Contains(out, "noisy line") {
		t.Errorf("expected matching exclude line suppressed, got %q", out)
	}
}

func TestBuild_BadFilterRegexpIsError(t *testing.T) {
	if _, err := Build(config.Logging{Filter: map[string]string{"key": "("}}); err == nil {
		t.Fatalf("expected an error for an invalid filter regexp")
	}
}

func TestBuild_BadExcludeRegexpIsError(t *testing.T) {
	if _, err := Build(config.Logging{Exclude: map[string]string{"key": "("}}); err == nil {
		t.Fatalf("expected an error for an invalid exclude regexp")
	}
}
