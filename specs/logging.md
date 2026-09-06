# Logging

Use `log/slog` (standard library) for all logging. There is no wrapping logging façade beyond what's described here — application code calls `slog` methods directly, through a logger obtained as described below.

## Configuration

- `logging.handler`: `JSON` uses `slog.NewJSONHandler`; `pretty` uses `github.com/lmittmann/tint`. User-facing behavior of `logging.handler`/`logging.level`/`logging.filter.*`/`logging.exclude.*` : `docs/content/configuration/operations.md ## Logging`.

## Logger construction

A `logging` package builds exactly one `*slog.Logger` at startup from the assembled `Config` (see `configuration.md`), and installs it via `slog.SetDefault`. This is the only place `logging.handler`/`logging.level` are read.

## Domain scoping

Every log line's `"module"` attribute (`docs/content/configuration/operations.md ## Logging`)
is attached once per package rather than repeated at every call site.

`logging.For(module string) *slog.Logger` returns a logger with `"module"` already attached.
Convention : one package-level `var log = logging.For("<name>")` per package, `<name>`
matching the package/directory name (`query`, `route`, `pg`, `dmut`, `boot`, `websec`, `static`,
`config`, `jwt`, `dbauth`, ...) — every log call in that package goes through `log`, not
`slog.Default()`/the bare `slog` package functions directly.

`For`'s own handler resolves `slog.Default()`'s CURRENT handler fresh on every log call, never a handler captured at construction time.

> Why : Go initializes every package-level `var` before `main()` runs, before `## Logger construction`'s `Install` has installed the real, configured handler. A naive `slog.Default().With("module", name)` would permanently freeze on the stdlib's built-in default handler.

Once `## Request-scoped logging`'s `logging.FromContext(ctx)` exists, request-scoped code
layers its own package's module tag on top of the request-scoped logger rather than using the
package-level `log` directly : `logging.FromContext(ctx).With("module", "route")` — `module` and
`request_id` compose freely, since both are just attributes on the same logger.

## Request-scoped logging

HTTP middleware, early in the chain, does the following for every incoming request:

1. Treats a blank `X-Request-Id` header the same as an absent one when deciding whether to generate a request ID (`docs/content/configuration/operations.md ## Logging` covers the header behavior itself).
2. Derives a child logger via `logger.With("request_id", id, ...)`, including other stable per-request attributes (e.g. route, remote address) as they're decided.
3. Stores that logger on the request's `context.Context`.

Application code MUST retrieve its logger from context (`logging.FromContext(ctx)`) rather than calling `slog.Default()` directly.

Code with no request context (startup, background jobs) uses `slog.Default()`, or a logger explicitly threaded through, tagged with a component/subsystem attribute instead of `request_id`.

## Access logging

The same middleware that derives the request-scoped logger logs the access line
(`docs/content/configuration/operations.md ## Logging`) once the response is written,
including for failed/panicking requests (status reflects the error response).

## Error integration with `samber/oops`

Per `error-handling.md`, errors are constructed/wrapped with `oops`, carrying structured context via its immutable `.With(key, value)` pattern.

When an `oops` error is logged, its attached context MUST be flattened into `slog.Attr`s, not stringified into the message — the same key attached at the error site must be queryable in JSON logs. A helper (`logging.Error(err) slog.Attr`, exact shape TBD) is responsible for this conversion; call sites use it instead of `slog.Any("error", err)`.

## Redaction

The same rule as configuration values (`configuration.md`, "Error handling and secrets") applies here: nothing considered a credential, token, or secret is ever written into a log line's value, including inside `oops` context attached via `.With(...)`. Call sites are responsible for not attaching secret values as log/error context in the first place — the logging layer does not attempt to guess or redact after the fact.
