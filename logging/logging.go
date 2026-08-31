// Package logging builds Rel's one process-wide *slog.Logger from
// config.Logging, per specs/logging.md ## Configuration and ## Logger
// construction — the only two sections this package implements.
//
// Deliberately NOT implemented here (documented, not silently dropped) :
// ## Request-scoped logging (request-ID middleware, logging.FromContext),
// ## Access logging (per-request log line), and ## Error integration with
// samber/oops (a logging.Error(err) slog.Attr helper) — these belong with
// the HTTP middleware/auth work that doesn't exist yet either, not a bare
// process entrypoint. See cmd/rel's own plan notes.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/ceymard/rel/config"
	"github.com/lmittmann/tint"
)

// Build constructs the *slog.Logger described by cfg, without installing it
// as the process default — see Install for that. Every field is validated :
// an unrecognized handler/level, or an invalid filter/exclude regexp, is a
// configuration error (specs/configuration.md's own "malformed value" is
// fatal), not silently ignored.
func Build(cfg config.Logging) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	var inner slog.Handler
	switch cfg.Handler {
	case "", "pretty":
		inner = tint.NewTextHandler(os.Stdout, &tint.Options{Level: level})
	case "JSON":
		inner = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	default:
		return nil, fmt.Errorf("logging: logging.handler: unrecognized value %q (want \"pretty\" or \"JSON\")", cfg.Handler)
	}

	filtered, err := newFilterHandler(inner, cfg.Filter, cfg.Exclude)
	if err != nil {
		return nil, err
	}

	return slog.New(filtered), nil
}

// Install builds cfg's logger and installs it via slog.SetDefault — the one
// place logging.handler/logging.level are read, per ## Logger construction.
// Returns the logger too, for callers that prefer passing it explicitly
// (e.g. into http.Server.ErrorLog via slog.NewLogLogger) rather than relying
// on the package-level default.
func Install(cfg config.Logging) (*slog.Logger, error) {
	logger, err := Build(cfg)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return logger, nil
}

// parseLevel maps logging.level's four accepted strings (default "info")
// onto slog.Level ; anything else is a configuration error.
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
		return 0, fmt.Errorf("logging: logging.level: unrecognized value %q (want debug/info/warn/error)", level)
	}
}

// filterHandler wraps another slog.Handler, applying ## Configuration's
// logging.filter.*/logging.exclude.* rules before delegating :
//
//   - Filter : for each key present in the filter map, a record whose
//     attributes INCLUDE that key must match the compiled regexp to be
//     emitted — a record that doesn't carry the key at all still passes
//     (the spec's own "log payloads that do not have anything to filter
//     against are displayed").
//   - Exclude : the same mechanism inverted (a matching record is
//     suppressed instead of required), applied after Filter.
//
// Caveat : only top-level attributes (a record's own Attrs() plus anything
// attached via logger.With(...)) are checked — a key nested inside an
// slog.Group is invisible to this check (the group as a whole stringifies
// to a single opaque value under the group's own key), so filtering/
// excluding on a grouped key silently behaves as "key absent" (passes
// through) rather than matching inside the group. Not fixed here since
// nothing in this package's current scope produces groups — worth
// revisiting if ## Request-scoped logging's request-ID middleware (still
// deferred, see this package's own doc comment) ever groups its attributes.
//
// attrs accumulates every attribute attached via logger.With(...)
// (WithAttrs), since those never appear in a Record's own Attrs() at Handle
// time — only attrs added directly to that specific call do. Without
// tracking accumulated attrs separately, filter/exclude keys set via
// .With() would be invisible to this check.
type filterHandler struct {
	inner   slog.Handler
	filter  map[string]*regexp.Regexp
	exclude map[string]*regexp.Regexp
	attrs   []slog.Attr
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
			return nil, fmt.Errorf("logging: %s.%s: invalid regexp: %w", label, key, err)
		}
		out[key] = re
	}
	return out, nil
}
