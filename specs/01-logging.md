# Logging

Use `log/slog` (standard library) for all logging. There is no wrapping logging façade beyond what's described here — application code calls `slog` methods directly, through a logger obtained as described below.

## Configuration

- `logging.handler` (`JSON` or `pretty`, default `pretty`) selects the output format.
  - `JSON` uses `slog.NewJSONHandler` — one JSON object per line, for production/machine consumption.
  - `pretty` uses `github.com/lmittmann/tint` — colored, human-readable single-line output, for local development.
- `logging.level` (`debug`, `info`, `warn`, `error`, default `info`) sets the minimum level emitted by the handler.
- `logging.filter.*` (default empty) for each key specified, a regexp on the values that it must contain to be displayed. Log payloads that do *not have* anything to filter against are displayed
- `logging.exclude.*` (default empty) similar to filter except will suppress the log entry. Applies *after* filter if specified.
- Output is always stdout. No file destinations, rotation, or multi-writer configuration — the process supervisor/container runtime owns log capture.

## Logger construction

A `logging` package builds exactly one `*slog.Logger` at startup from the assembled `Config` (see `01-configuration.md`), and installs it via `slog.SetDefault`. This is the only place `logging.handler`/`logging.level` are read.

## Domain scoping

On top of logging level

## Request-scoped logging

HTTP middleware, early in the chain, does the following for every incoming request:

1. Reads a request ID from an inbound header (name TBD — e.g. `X-Request-Id`), or generates one if absent/blank.
2. Derives a child logger via `logger.With("request_id", id, ...)`, including other stable per-request attributes (e.g. route, remote address) as they're decided.
3. Stores that logger on the request's `context.Context`.

Application code MUST retrieve its logger from context (`logging.FromContext(ctx)`) rather than calling `slog.Default()` directly, so every line emitted while handling a request carries `request_id` and can be correlated end to end.

Code with no request context (startup, background jobs) uses `slog.Default()`, or a logger explicitly threaded through, tagged with a component/subsystem attribute instead of `request_id`.

## Access logging

The same middleware logs one line per completed request, at `info` level, once the response is written — at minimum: method, path, status code, duration, and response size. This happens unconditionally, including for failed/panicking requests (status reflects the error response); it is not something individual handlers opt into.

## Error integration with `samber/oops`

Per `02-error-handling.md`, errors are constructed/wrapped with `oops`, carrying structured context via its immutable `.With(key, value)` pattern.

When an `oops` error is logged, its attached context MUST be flattened into `slog.Attr`s, not stringified into the message — the same key attached at the error site must be queryable in JSON logs. A helper (`logging.Error(err) slog.Attr`, exact shape TBD) is responsible for this conversion; call sites use it instead of `slog.Any("error", err)`.

## Redaction

The same rule as configuration values (`01-configuration.md`, "Error handling and secrets") applies here: nothing considered a credential, token, or secret is ever written into a log line's value, including inside `oops` context attached via `.With(...)`. Call sites are responsible for not attaching secret values as log/error context in the first place — the logging layer does not attempt to guess or redact after the fact.
