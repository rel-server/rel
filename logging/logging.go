// Package logging builds rel's process-wide logger, per-request loggers and the access log.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/rel-server/rel/config"
	"github.com/samber/oops"
)

// Error returns err as slog key/value args : "error", then every oops .With context entry in err's chain as a
// top-level pair, never grouped, so logging.filter/logging.exclude can match them.
func Error(err error) []any {
	if err == nil {
		return nil
	}
	attrs := []any{"error", err.Error()}
	if oopsErr, ok := oops.AsOops(err); ok {
		for k, v := range oopsErr.Context() {
			attrs = append(attrs, k, v)
		}
	}
	return attrs
}

// Build constructs cfg's logger writing to w, without installing it as the default. An unrecognized handler/level or
// an invalid filter/exclude regexp is an error.
func Build(w io.Writer, cfg config.Logging) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	var inner slog.Handler
	switch cfg.Handler {
	case "", "pretty":
		inner = newPrettyHandler(w, level, colorEnabled())
	case "JSON":
		inner = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	default:
		return nil, oops.With("handler", cfg.Handler).Errorf("logging: logging.handler: unrecognized value %q (want \"pretty\" or \"JSON\")", cfg.Handler)
	}

	filtered, err := newFilterHandler(inner, cfg.Filter, cfg.Exclude)
	if err != nil {
		return nil, err
	}

	return slog.New(filtered), nil
}

// colorEnabled is whether the pretty handler colors its output : always, unless NO_COLOR is set non-empty.
func colorEnabled() bool {
	return os.Getenv("NO_COLOR") == ""
}

// Install builds cfg's logger on stdout, sets it as slog's default and returns it.
func Install(cfg config.Logging) (*slog.Logger, error) {
	logger, err := Build(os.Stdout, cfg)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return logger, nil
}

// For returns a logger tagged with module, meant for one package-level `var log = logging.For("<package>")` per
// package. Safe to build before Install : it uses slog's default handler as of each log call.
func For(module string) *slog.Logger {
	return slog.New(dynamicHandler{attrs: []slog.Attr{slog.String("module", module)}})
}

// dynamicHandler resolves slog.Default()'s handler on each call.
type dynamicHandler struct {
	attrs []slog.Attr
}

func (h dynamicHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return slog.Default().Handler().Enabled(ctx, level)
}

func (h dynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	handler := slog.Default().Handler()
	if len(h.attrs) > 0 {
		handler = handler.WithAttrs(h.attrs)
	}
	return handler.Handle(ctx, r)
}

func (h dynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return dynamicHandler{attrs: merged}
}

// WithGroup is a no-op : nothing in rel opens groups on these loggers.
func (h dynamicHandler) WithGroup(name string) slog.Handler {
	return h
}

// parseLevel maps logging.level (default "info") onto slog.Level.
func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, oops.With("level", level).Errorf("logging: logging.level: unrecognized value %q (want debug/info/warn/error)", level)
	}
}

// filterHandler applies logging.filter/logging.exclude ;
// a key nested inside an slog.Group is invisible, treated as absent.
type filterHandler struct {
	inner   slog.Handler
	filter  map[string]*regexp.Regexp
	exclude map[string]*regexp.Regexp

	// attrs accumulates WithAttrs attributes, which the record does not carry.
	attrs []slog.Attr
}

func newFilterHandler(inner slog.Handler, filter, exclude map[string]string) (*filterHandler, error) {
	compiledFilter, err := compileAll("logging.filter", filter)
	if err != nil {
		return nil, err
	}
	compiledExclude, err := compileAll("logging.exclude", exclude)
	if err != nil {
		return nil, err
	}
	return &filterHandler{inner: inner, filter: compiledFilter, exclude: compiledExclude}, nil
}

func (h *filterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *filterHandler) Handle(ctx context.Context, r slog.Record) error {
	if len(h.filter) > 0 || len(h.exclude) > 0 {
		values := make(map[string]string, len(h.attrs)+r.NumAttrs())
		for _, a := range h.attrs {
			values[a.Key] = a.Value.String()
		}
		r.Attrs(func(a slog.Attr) bool {
			values[a.Key] = a.Value.String()
			return true
		})
		for key, re := range h.filter {
			if v, ok := values[key]; ok && !re.MatchString(v) {
				return nil
			}
		}
		for key, re := range h.exclude {
			if v, ok := values[key]; ok && re.MatchString(v) {
				return nil
			}
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *filterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &filterHandler{inner: h.inner.WithAttrs(attrs), filter: h.filter, exclude: h.exclude, attrs: merged}
}

func (h *filterHandler) WithGroup(name string) slog.Handler {
	return &filterHandler{inner: h.inner.WithGroup(name), filter: h.filter, exclude: h.exclude, attrs: h.attrs}
}

func compileAll(label string, raw map[string]string) (map[string]*regexp.Regexp, error) {
	out := make(map[string]*regexp.Regexp, len(raw))
	for key, pattern := range raw {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, oops.With("key", label+"."+key).Wrapf(err, "logging: %s.%s: invalid regexp", label, key)
		}
		out[key] = re
	}
	return out, nil
}
