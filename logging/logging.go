// Package logging builds Rel's one process-wide *slog.Logger from
// config.Logging (## Configuration, ## Logger construction), derives
// per-request child loggers (## Request-scoped logging, request.go :
// RequestMiddleware, FromContext), and provides ResponseRecorder
// (response_recorder.go), the http.ResponseWriter wrapper route/handler.go
// and server/rel.go each use to build ## Access logging's own line —
// ## Configuration, ## Logger construction, ## Domain scoping,
// ## Request-scoped logging, and ## Error integration with samber/oops
// (Error, below) live here ; the rest of specs/logging.md's sections
// (## Access logging, ## Introspection logging, ## Route registry logging,
// ## Error logging) live in the packages they log (route, server, pg)
// instead, since their fields are domain-specific.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/rel-server/rel/config"
	"github.com/lmittmann/tint"
	"github.com/samber/oops"
)

// Error returns err as a flat run of alternating key/value args, meant to be
// spread into a slog call (logger.Error("...", logging.Error(err)...)) :
// "error" holds err.Error() itself, followed by every oops .With(key, value)
// context entry attached anywhere in err's chain, as its own sibling
// key/value pair — never nested under an "error"/"context" group, since
// specs/logging.md ## Error integration with samber/oops requires a key
// attached at the error site to be independently queryable (and
// logging.filter/logging.exclude, ## Configuration, can't match a key
// hidden inside a group at all).
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

// For returns a *slog.Logger tagged with a "module" attribute — see
// docs/content/configuration/operations.md ## Logging for what the
// attribute means to a reader of the logs, specs/logging.md ## Domain
// scoping for the attach-once-per-package mechanism. Convention : one
// package-level `var log = logging.For("<name>")` per package, `<name>`
// matching the package/directory name (query, route, pg, reloadcmd, boot, ...),
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
