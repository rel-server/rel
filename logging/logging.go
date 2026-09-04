// Package logging builds Rel's one process-wide *slog.Logger from
// config.Logging (## Configuration, ## Logger construction), and derives
// per-request child loggers (## Request-scoped logging, request.go :
// RequestMiddleware, FromContext) — three of specs/logging.md's five
// sections.
//
// Deliberately NOT implemented here (documented, not silently dropped) :
// ## Access logging (per-request summary log line) and ## Error
// integration with samber/oops (a logging.Error(err) slog.Attr helper) —
// no concrete driving need for either yet, unlike request-scoped logging
// itself (specs/TODO.md tracked it as a real gap until it landed).
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

// For returns a *slog.Logger tagged with a "module" attribute — the
// mechanism specs/logging.md ## Domain scoping describes : "logging must
// show what module it came from... to help with context." Convention : one
// package-level `var log = logging.For("<name>")` per package, `<name>`
// matching the package/directory name (query, route, pg, dmut, boot, ...),
// used for every log call in that package instead of calling slog.Default()
// or the slog package funcs directly.
//
// Deliberately safe to store in a package-level var regardless of
// initialization order : Go runs package-level var initializers before
// main() ever gets a chance to call Install, so a naive
// `slog.Default().With("module", name)` here would permanently freeze in
// whatever handler slog.Default() happened to return AT THAT MOMENT — the
// stdlib's own built-in default, not rel's configured pretty/JSON one
// Install sets up later. For's own handler (dynamicHandler, below) instead
// resolves slog.Default()'s CURRENT handler fresh on every single log call,
// so a logger built before Install runs still picks up the real handler
// once it exists.
func For(module string) *slog.Logger {
	return slog.New(dynamicHandler{attrs: []slog.Attr{slog.String("module", module)}})
}

// dynamicHandler holds only the extra attrs a For(...) logger has
// accumulated, resolving slog.Default()'s handler fresh on each call — see For.
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

// WithGroup is a no-op — nothing in this package's scope produces groups ;
// see filterHandler's own doc comment for the same limitation.
func (h dynamicHandler) WithGroup(name string) slog.Handler {
	return h
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

// filterHandler applies logging.filter/logging.exclude (## Configuration) ;
// a key nested inside an slog.Group is invisible, treated as absent.
type filterHandler struct {
	inner   slog.Handler
	filter  map[string]*regexp.Regexp
	exclude map[string]*regexp.Regexp

	// attrs accumulates logger.With(...) attributes, which a Record's own
	// Attrs() at Handle time doesn't include on its own.
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
			return nil, fmt.Errorf("logging: %s.%s: invalid regexp: %w", label, key, err)
		}
		out[key] = re
	}
	return out, nil
}
