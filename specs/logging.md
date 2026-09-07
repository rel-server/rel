# Logging

Use `log/slog` (standard library) for all logging. There is no wrapping logging façade beyond what's described here — application code calls `slog` methods directly, through a logger obtained as described below.

## Configuration

- `logging.handler`: `JSON` uses `slog.NewJSONHandler`; `pretty` uses `github.com/lmittmann/tint`. User-facing behavior of `logging.handler`/`logging.level`/`logging.filter.*`/`logging.exclude.*` : `docs/content/configuration/operations.md ## Logging`.

## Logger construction

A `logging` package builds exactly one `*slog.Logger` at startup from the assembled `Config` (see `docs/content/configuration/index.md`), and installs it via `slog.SetDefault`. This is the only place `logging.handler`/`logging.level` are read.

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

`route/handler.go`'s `handleRoute` and `server/rel.go`'s `handleRel` each emit exactly one `info`-level line per HTTP request they handle, via `logging.FromContext(ctx)`, regardless of outcome (success, a rejected/unauthorized request, a malformed request, a Postgres error).

The line is emitted from a `defer` registered at the top of the function, after wrapping the incoming `http.ResponseWriter` in a small recorder that captures the status code (from the first `WriteHeader`/implicit-200-on-first-`Write`) and the total bytes written — every downstream response helper (`writePlainError`, `writeErrorForPgErr`, `writeSingleReturnResponse`, `writeError`, ...) keeps calling the ordinary `http.ResponseWriter` methods on that recorder unchanged.

> Why a `defer` : an early rejection (anonymous access disabled, malformed body, a route not found for the resolved role) returns from several different points in the function. A `defer` is the only way to guarantee the line still fires from every one of them, instead of duplicating a log call at each `return`.

Fields common to both `handleRoute` and `handleRel`:

- `method`, `path`
- `status` — from the response recorder
- `response_size` — bytes written, from the response recorder
- `duration` — elapsed time since the function was entered
- `verified` — whether the request's JWT verified (`handleRoute` : `jwtpkg.VerifyRequest`'s second return value ; `handleRel` : the second return value of `jwtpkg.FromContext`, since verification already ran in `jwt.Middleware`)
- `role` — the resolved Postgres role ; the zero value (empty string) when the request was rejected before role resolution ran. `applyRole` (`server/rel.go`) MUST return (or otherwise surface) the role it resolved, not just apply it, so `handleRel` has a value to attach here
- `claims` — the full JWT claims map (`jwtpkg.Claims`), logged as-is with no field held back or redacted ; empty/absent for an unverified request

> Why claims are logged whole, not reduced to `role`/`sub` : any free-form claim a login function attached (a `user_id`, a tenant ID, ...) can be the exact detail an operator is trying to correlate a request against. `## Redaction` below is the only thing that ever holds a field back, and a JWT claim is not a credential/token/secret under that rule.

Fields specific to `handleRoute` (`route/handler.go`):

- `function` — `route.Function.Identifier.String()`
- `full_control` — `route.FullControl`

Fields specific to `handleRel` (`server/rel.go`):

- `item_count` — the number of resolved `Sequence` items (1 for a bare, non-`Sequence` request)
- `write` — `true` if any resolved item is a write (`resolvedItem.isWrite`)

A field not yet known at the point the `defer` runs (e.g. `role`/`claims` for a request rejected before JWT verification even started) is logged as its zero value (empty string, empty map) rather than omitted.

### Per-statement detail (`debug`)

The `info` line above identifies *that* a request ran a query, not *what* it ran. At `debug`, every SQL statement actually sent to Postgres while handling the request is logged, with its full text and parameters, no reduction or redaction — the same no-redaction policy `## Access logging`'s `claims` field already applies. One resolved item can send several statements (a write item's own DML plus its `_data` bookkeeping), so this is one line per statement, not one line per item :

- `handleRel` (`server/rel.go`) : every `Exec`/`Query`/`CopyFrom` call a write item makes is logged by wrapping the `query.Querier` passed to `ExecuteWriteStateParamsOpts` — the only place that sees all of a write's internal statements, not just its outermost one. A read item's own single `select` is logged where it's issued (`streamItem`, `executeDiscard`).
- `handleRoute` (`route/handler.go`) : the invoked function's compiled call (`buildInvokeCall`'s SQL text and args), logged where `invokeSingleReturnRoute`/`invokeFullControlRoute` issue it.

> Why full SQL/params at `debug` rather than just names/shapes : the same reasoning as `## Access logging`'s `claims` field — an operator debugging a specific request needs the literal values, and `logging.filter`/`logging.exclude` (`## Configuration`) is the mechanism for narrowing that down, not redaction at the source.

## Introspection logging

`pg/info.go`'s `introspect` — the shared body behind both `NewInfosAdminQuery` (startup) and `ReIntrospect` (every `SIGUSR1` reload) — emits exactly one `info`-level line, via `pg`'s own `logging.For("pg")` logger, after `DbInfos.Fill` returns successfully:

- `relation_count`, `function_count`, `type_count` — `len(db.Relations)`, `len(db.Functions)`, `len(db.Types)`
- `anonymous_role_exists` — `db.AnonymousRoleExists`

This fires once per full introspection, never per pooled connection.

`DbInfos.Fill`'s three symbol-discovery steps (`FillRelationInformations`, `FillFunctionInformations`, `FillTypeInformations`) each emit one `debug`-level line per entry they add, once that step's own slice is fully populated :

- Per relation : `schema`, `name`, `column_count` (`len(Relation.Columns)`)
- Per function : `schema`, `name`, `arity` (argument count)
- Per type : `schema`, `name`

> Why per-entry rather than one combined dump : `logging.filter`/`logging.exclude` (`## Configuration`) match a single line's attributes, so one line per symbol is what makes "only show me what introspection found under schema X" filterable at all.

## Route registry logging

`route/routeset.go`'s `BuildRegistry` emits one `info`-level line once its final `routes`/`middleware` slices are settled (after `excludeCollisions`, sorting, and `applyAnonymousAuthorization`) :

- `route_count`, `middleware_count`

It also emits one `debug`-level line per entry in each of those final slices — the routes and middleware that survived every exclusion (invalid declaration, reserved path, collision) and are actually being exposed :

- `methods`, `path` (`Route.Methods`, `Route.AnonPath`), `function` (`Route.Function.Identifier.String()`), `anonymous_authorized` (`Route.AnonymousAuthorized`)

The existing `warn`/`error` lines for a rejected/disabled/colliding declaration (`BuildRegistry`, `excludeCollisions`, `applyAnonymousAuthorization`) remain as they are.

## Error integration with `samber/oops`

Per `AGENTS.md`'s Golang error-handling rule, errors are constructed/wrapped with `oops`, carrying structured context via its immutable `.With(key, value)` pattern.

When an `oops` error is logged, its attached context MUST be flattened into sibling `slog` attributes, not stringified into the message and not nested under a group — the same key attached at the error site must be independently queryable in JSON logs, and `logging.filter`/`logging.exclude` (`## Configuration`) can't match a key hidden inside a group at all. `logging.Error(err) []any` (`logging/logging.go`) does this conversion : `"error"` holds `err.Error()` itself, followed by every `oops` `.With(key, value)` context entry attached anywhere in `err`'s chain, each as its own key/value pair. Call sites spread it into the log call (`logger.Error("...", logging.Error(err)...)`) instead of using `slog.Any("error", err)`.

## Error logging

A request that ends in a `5xx` is logged in full server-side, regardless of what the client-facing response itself shows — `dev`/tier gating (`## Postgres error detail`, `error-handling.md`) governs the *response* only, never whether the server records what actually happened.

- `server/response.go`'s `writeError` and `route/response.go`'s `writeErrorForPgErr` each log one `error`-level `"request failed"` line, via `logging.FromContext(ctx)`, for every status `>= 500` they write — before composing the (possibly redacted) client response. Fields : `code`, `status`, `logging.Error(err)...`, plus `pg_error` (the full, untiered `pgerr.Detail`) when the error classified as a Postgres error, regardless of whether that detail was allowed into the response.
- A `4xx` is not separately logged here — the response body already carries the full detail (`error-handling.md` : a `4xx` is always shown in full, never redacted), and `## Access logging`'s one line per request already records its `status`.

> Why gated on status rather than always logging every `writeError`/`writeErrorForPgErr` call : a `4xx` is client input, not a server fault — logging it again would just duplicate what the response and the access log already carry, for every malformed request a client happens to send.

`route/response.go`'s other error path, `writePlainError`, takes a plain message string rather than an `error` — most of its call sites (`route/handler.go`, `route/middleware.go`, `route/upload_handler.go`) already reduced the underlying error to a static string (`"acquiring connection"`, `"starting transaction"`, ...) before calling it, so there is no `error` left for it to log. Logging those in full would mean threading the original `error` through to each call site first, a larger, call-site-by-call-site change not made here.

## Redaction

The same rule as configuration values (`docs/content/configuration/index.md ## Secrets and generated values`) applies here: nothing considered a credential, token, or secret is ever written into a log line's value, including inside `oops` context attached via `.With(...)`. Call sites are responsible for not attaching secret values as log/error context in the first place — the logging layer does not attempt to guess or redact after the fact.

A JWT claim (`## Access logging`) is not a credential/token/secret under this rule — the signed token string itself is, and is never logged (`jwt/middleware.go`'s verify-failure line already only logs `path`/`error`, never the token).
