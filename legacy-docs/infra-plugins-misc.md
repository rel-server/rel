# Legacy Server: Infra, Plugins, and Miscellaneous Reference

Source: `sales-way.com/server` at `/home/chris/Code/rel/_legacy`. All paths below are relative to that
directory unless stated otherwise.

## 1. `sw.SwServer` — the shared app-context struct

Defined in `sw/defs.go:18-29`. This struct is instantiated once in `main.go` (`var srv sw.SwServer`,
not a pointer) and threaded by pointer (`&srv`) through essentially every subsystem (auth, dmut,
plugins, websocket, query engine).

```go
type SwServer struct {
    Router                  chi.Router
    Pool                    *pgxpool.Pool
    Hostnames               []string
    Port                    string
    Schemas                 []string
    Tables                  pg.DBAllTables
    Functions                pg.DBFunctionMap
    DmutStatus              string
    RootScope               *query.Scope
    AvailableLoginFunctions LoginFunctions
}
```

### Fields (`sw/defs.go:18-29`)

| Field | Type | Set at | Purpose |
|---|---|---|---|
| `Router` | `chi.Router` | `main.go:87` (`chi.NewRouter()`) | The single top-level HTTP router; all route registration and middleware (`Use(...)`) throughout the codebase mutate this. |
| `Pool` | `*pgxpool.Pool` | `main.go:102` | pgx connection pool to Postgres. May be `nil` if no DB URL was configured (server can run "DB-less", see `HasPg()`). |
| `Hostnames` | `[]string` | `main.go:106`, split from `VIRTUAL_HOST` env var via a regex splitter | List of hostnames the server considers itself reachable under. Used to register one OAuth provider callback per hostname (`oauth.go:50-58`, looping `register(...)` calls for google/salesforce/yahoo per hostname) and to build the per-hostname SAML root/callback URLs and self-signed certificate (`saml.go:166,199`, `generateSelfSignedCertificate(srv.Hostnames)`). It is the multi-hostname/multi-tenant hook for auth callback URLs, not a cookie/CORS mechanism. |
| `Port` | `string` | `main.go:107`, hardcoded `"3001"` | The listen port passed to `http.ListenAndServe(":"+srv.Port, ...)`. Note: **not configurable via env var** — always `3001` (matches `EXPOSE 3001` in the Dockerfile). |
| `Schemas` | `[]string` | `main.go:85`, split from `PGRST_DB_SCHEMA` (default `"public"`) | Postgres schemas the server introspects/exposes; shared with PostgREST via the same env var. |
| `Tables` | `pg.DBAllTables` (`map[string]*pg.DBTable`, `pg/types.go:188`) | `PgReloadTables()` | Cache of introspected DB tables, refreshed on demand. |
| `Functions` | `pg.DBFunctionMap` (`map[string]*pg.Function`, `pg/types.go:189`) | `PgReloadTables()` | Cache of introspected DB functions. |
| `DmutStatus` | `string` | `dmut.go:36,46,49,53` | Human-readable status of the last dmut (DB migration tool) run: `"ok"`, `"skipped"`, or an error message. Surfaced verbatim on the `/heartbeat` endpoint (`main.go:320-337`). |
| `RootScope` | `*query.Scope` | `PgReloadTables()`, via `query.NewRootScope(srv.Tables, srv.Functions, srv.Schemas)` (`query/scope.go:206`) | Root of the query-building/scoping system derived from the introspected schema; used by the SQL query layer (out of scope for this doc). |
| `AvailableLoginFunctions` | `LoginFunctions` (struct, `sw/defs.go:31-35`) | `ReloadLoginFunctions()` (`auth.go:109-121`) | Tri-state capability flags (`Login`, `ExternalLogin`, `SessionCheck`, all `bool`, JSON/db-tagged) reflecting which Postgres-side auth functions actually exist in the DB. Drives whether password/external login endpoints respond or 403. |

`LoginFunctions` (`sw/defs.go:31-35`):
```go
type LoginFunctions struct {
    Login         bool `json:"login" db:"login"`
    ExternalLogin bool `json:"external_login" db:"external_login"`
    SessionCheck  bool `json:"session_check" db:"session_check"`
}
```
Populated by a single query (`SQL_CHECK_FUNCTIONS` in `auth.go`) that scans Postgres's function catalog for
specifically-named routines. If `SessionCheck` is false, session checking is disabled server-wide (logged as
a warning, `auth.go:117-120`). Also reloaded on `SIGUSR1` after a dmut re-run (`main.go:305-312`).

### Methods

All defined in `sw/defs.go` (`(srv *SwServer)` receiver):

- `LogInfo(msg ...interface{})` (`sw/defs.go:45-48`) — prefixes with a green `"· "` and calls `log.Print`.
- `LogError(msg ...interface{})` (`sw/defs.go:50-53`) — prefixes with a red bold `"⚠ "`.
- `LogWarn(msg ...interface{})` (`sw/defs.go:55-58`) — prefixes with a yellow bold `"⚠ "` (**same glyph as
  `LogError`**, only color differs — see Notes section).
- `HasPg() bool` (`sw/defs.go:60-62`) — `srv.Pool != nil`. The idiomatic way to check whether the server is
  running in "no database" mode.
- `PgSimpleExec(ctx, sql, args...) error` (`sw/defs.go:64-77`) — acquires a connection from `Pool`, runs
  `Exec`, releases. Returns an error if `Pool` is nil.
- `PgSimpleQuery(ctx, sql, args...) (pgx.Rows, error)` (`sw/defs.go:79-92`) — same pattern for `Query`.
- `PgSimpleQueryRow(ctx, sql, args...) (pgx.Row, error)` (`sw/defs.go:94-106`) — same pattern for `QueryRow`.
- `PgReloadTables() error` (`sw/defs.go:108-113`) — re-introspects `Tables`/`Functions` via
  `pg.ReloadTables(srv.Pool, srv.Schemas)` and rebuilds `RootScope`. Called at boot and after dmut runs
  triggered by `SIGUSR1`.

**Likely bug in `PgSimpleQuery`/`PgSimpleQueryRow`**: both acquire a pooled connection, `defer
conn.Release()` (or `pool.Release()`), and then `return rows, err` / `return pool.QueryRow(...), nil`
(`sw/defs.go:84-91`, `99-105`). Because the `defer` fires when the function *returns*, the connection is
handed back to the pool **before the caller ever reads from `rows`/`row`** — i.e. `rows.Scan(...)` or
row iteration happens against a connection that pgx may already be reusing for another request.
`PgSimpleExec` (`sw/defs.go:64-77`) does not have this problem because `Exec` fully completes before the
deferred `Release()` runs. All three current call sites (`auth.go:110-114`, `jwt.go:265`,
`websocket.go:105`, `websocket-session.go:152`) use `PgSimpleQueryRow` and immediately `.Scan(...)` the
result — worth verifying under load in the rewrite; this pattern should almost certainly acquire, query,
scan/iterate, *then* release, all within the helper. Note also `auth.go:110-114` logs-and-continues on a
`PgSimpleQueryRow` error rather than returning, so a non-nil `err` there can lead to `rows.Scan(...)` being
called on a `nil` row.

Dangling comment at `sw/defs.go:115-117` (`// GetEnvOrDefault`, `// SetRoleCookie`, `// UnsetCookie`) lists
method names that do **not** exist on `SwServer` — likely a stale TODO/reminder left by the author, or
methods that were moved out to free functions elsewhere (`getenvOrDefault` is indeed a free function in
`env.go`). Worth resolving one way or the other in the rewrite (either implement them on the context object
or delete the comment).

### `sw/general.go` — a second, package-level logging API

`sw/general.go:14-27` defines **free functions** `LogInfo`, `LogError`, `LogWarn` (no receiver) that
duplicate the `SwServer` methods almost exactly, but with different glyphs/colors (`"* "` green, `"error "`
red, `"? "` yellow) and no message aggregation logic beyond `append`. It also defines `Error(msg string, err
error) error` (`sw/general.go:8-12`), a `fmt.Errorf`-based wrapper with a commented-out `// LogError(err)`
call (dead code). These free functions appear to be an older/parallel logging surface to the `SwServer`
methods — grep shows both are used across the codebase. **This is a duplication smell**: two logging APIs
with the same names but different prefixes/colors, one bound to the server struct and one not.

## 2. Plugin system (`plugins.go`)

`registerPlugins(srv *sw.SwServer)` (`plugins.go:15-62`), called once from `main.go:204` after the router
and DB pool are set up.

Mechanism — Go's native `plugin` package (`.so` shared objects), **Linux-only, CGO-dependent** in general
(though this project's normal build uses `CGO_ENABLED=0`, which is notable — see Notes):

1. Scans three directories for `*.so` files (`plugins.go:13,19-34`): `/plugins`, `./plugins`, and whatever
   `SW_PLUGINS_DIR` env var points to. Non-existent directories are silently skipped (`os.ReadDir` error
   ignored).
2. For each `.so` found, calls `plugin.Open(path)` (`plugins.go:38`).
3. Looks up a symbol named `SwInit` (`plugins.go:44`).
4. Type-asserts it to `func(*sw.SwServer) error` (`plugins.go:50`).
5. Calls it with the live `srv` pointer, logging via `srv.LogInfo` (`plugins.go:56`) before invocation.
6. Any error at any step (open failure, missing symbol, wrong signature, or the plugin's own returned
   error) is logged with `log.Print("!! ", ...)` and the plugin is skipped — **failures are non-fatal**, the
   server keeps booting.

Because a plugin receives the full `*sw.SwServer`, it can register additional routes on `srv.Router`, read
`srv.Pool`, etc. — i.e. it has essentially unrestricted access to the running server.

**No plugins currently ship in this repository.** A grep for `SwInit` across the whole tree only finds the
lookup site itself (`plugins.go:44,52`) — there is no example plugin, no `plugins/` directory, and no build
target that produces a `.so`. The mechanism is fully wired up but currently unused/undemonstrated in this
codebase (plugin implementations presumably live in a separate, closed repo, or the feature is aspirational).

## 3. Panic recovery and error responses

### `RecovererColored` middleware (`recoverer.go:14-37`)

Registered in the middleware chain at `main.go:146` (order: `RequestID`, `RealIP`, `Logger`,
**`RecovererColored`**, `jwtRefreshCookieMiddleware`, ...).

Behavior on panic:
- Captures `runtime/debug.Stack()` and passes it through `filterStack` (`recoverer.go:18`, `recoverer.go:40-71`).
- `filterStack` walks the raw stack two lines at a time (function line + file:line), color-highlights the
  *first* frame in bold red/white, and colors the rest cyan/gray. **The filtering logic that was meant to
  skip framework noise (net/http, chi, runtime, testing) is entirely commented out** (`recoverer.go:49-55`)
  — so in practice every frame, including stdlib/chi internals, is printed. This is dead/disabled code, not
  removed.
- Prints a `=== PANIC RECOVERED ===` banner plus the request path and the full colored stack trace to
  **stdout only** (`fmt.Println`, not `srv.LogError`) — `recoverer.go:24-29`.
- Responds to the client with `http.Error(w, http.StatusText(http.StatusInternalServerError), 500)`
  (`recoverer.go:31`) — i.e. **plain-text `"Internal Server Error"` body, no stack trace, no JSON**. The
  client-facing response leaks nothing; the stack trace only goes to server logs/stdout.

### `ReplyError` (`error_pages.go:11-22`) — the shared error-response contract

Signature: `ReplyError(w http.ResponseWriter, code int, short string, desc string)`.

Contract:
- Sets `Cache-Control: no-cache, no-store, must-revalidate` (`error_pages.go:13`).
- Writes the given `code` as the HTTP status (`error_pages.go:14`).
- Body is **HTML**, not JSON, rendered from a `fasttemplate` template (`tpl_error`, `error_pages.go:24-87`)
  embedded in the source using `{{ }}` delimiters. The template renders a full standalone HTML page
  (`<!doctype html>`, links `/css/pgrel.css`, includes an inline SVG "Sales Way" logo) showing the status
  code, an HTML-escaped `short` message, an HTML-escaped `desc` message, and a fixed
  "Contact the administrator if the issue persists" line.
- Both `short` and `desc` are passed through `html.EscapeString` before templating (`error_pages.go:18-19`)
  — no XSS via these fields, but note that several call sites interpolate raw `err.Error()` values directly
  into `desc` (e.g. `main.go:230`, `saml.go:236`) — those are still escaped by `ReplyError` itself, so this
  is safe, but it does mean **raw internal error strings (potentially including SQL errors, file paths,
  etc.) are shown to the end user** on 401/403/500 pages. Flag for the rewrite: decide whether internal
  error detail belongs in end-user-facing pages at all, even escaped.
- No `Content-Type` header is explicitly set for `ReplyError` (relies on Go's default sniffing, which will
  usually infer `text/html` from the `<!doctype html>` prefix, but this is implicit rather than declared).

Used across the codebase for user-facing HTTP errors: `main.go:230,238,274` (500/404 catch-alls), `auth.go`
(login-disabled 403, bad-credentials 401, cookie-set failure 500, malformed request 400), `jwt.go` (five
distinct 401 "Unauthorized" variants for missing cookie/role/session/marshal-failure/session-check-failure),
`saml.go` (SAML processing 500/401/503).

### `_printStackTrace` (`stacktrace.go:12-73`) — a **separate**, more detailed error renderer for SQL errors

Distinct from `ReplyError`. Used only in the Postgres request-forwarding layer (`pg2.go` — 7 call sites at
lines 76, 92, 207, 229, 235, 276, 296; `pg3.go` — 3 call sites at lines 30, 39, 57).

Behavior:
- Always responds with **HTTP 400** (`stacktrace.go:20`), regardless of the actual error's nature — this is
  a fixed status, not parameterized like `ReplyError`.
- Renders an HTML page (own inline template, also linking `/css/pgrel.css`) showing the HTML-escaped
  `err.Error()` as a header.
- **Only if `sql != "" && DEBUG_ENABLED`** (`stacktrace.go:34`, `DEBUG_ENABLED` = `SW_ENABLE_DEBUG=="true"`,
  `env.go:44`) does it also render the raw SQL query text (escaped) in a `<pre>` block. So the SQL text leak
  is gated behind a debug flag — good.
- **Not gated behind `DEBUG_ENABLED`**, however, is the stack trace table itself (file/line/function per
  frame) — this renders unconditionally whenever the error satisfies `github.com/pkg/errors`'
  `StackTrace() []uintptr` interface (`stacktrace.go:14-16,47-63`). **Security note: full server-side file
  paths and function names from the stack trace are sent to the client on every such error, in all
  environments, not just debug/dev.** This is a more significant information disclosure than
  `RecovererColored` (which never sends stack traces to the client) or `ReplyError` (which sends no stack
  trace at all). Worth flagging explicitly for the rewrite — this should almost certainly be gated behind
  `DEBUG_ENABLED` (or removed entirely from production responses) just like the SQL text already is.

## 4. Versioning (`version.go`)

- `VERSION = "0.7.6"` (`version.go:10`) — a **hardcoded string constant** in source, not injected via
  `-ldflags` or derived from `git describe` at build time. The `Makefile` *reads* this constant back out via
  `grep`/`grep -oP` to compute the Docker image tag (`Makefile:5`: `TAG = $(shell cat version.go | grep
  VERSION | grep -oP "(\d+\.?)+" )`) — so bumping the release version means manually editing this one line,
  and the Makefile scrapes it back for tagging. There is no build-time version stamping mechanism at all.
- `VERSION` is used in exactly one place at runtime: logged once at boot (`main.go:115`,
  `srv.LogInfo("goserver version ", VERSION)`). It is **never sent to the client** (no header, no JSON
  field, no endpoint exposes it).
- `getClientVersion(srv *sw.SwServer)` (`version.go:14-50`) is called once at boot (`main.go:89`), **before**
  the DB pool is even set up. It does not read any HTTP request/header from an actual client — instead it:
  1. Stats the bundled frontend bundle file at `/static/app.js` (the compiled SPA).
  2. Seeks to the last 64 bytes of that file.
  3. Regex-searches for a `//!(v...)` comment marker (a version marker the frontend build presumably
     injects into the tail of its bundle).
  4. If found, stores it in a package-level `var clientVersion = ""` (`version.go:12`) and logs it
     (`version.go:47-48`).
- **`clientVersion` is otherwise unused** — no gate, no comparison against `VERSION`, no HTTP header check,
  no endpoint returns it. Despite the name suggesting a frontend/backend compatibility check, there is
  currently **no actual enforcement or comparison logic anywhere in the codebase** — it's read once, logged,
  and never consulted again. This looks like a half-built feature (or a check that was removed and the
  scaffolding left behind). Worth deciding for the rewrite whether real compat gating is wanted, and if so,
  building the comparison (e.g. reject requests / warn if `clientVersion` doesn't match a server-declared
  minimum), since right now it's dead weight.

## 5. `utils.Set[T]` and `utils.MapSet[K,V]` (`utils/set.go`)

Generic (Go 1.18+ type parameters) set built on `map[T]struct{}`:

```go
type Set[T comparable] struct {
    Map  map[T]struct{}
    Size uint
}
```
- `Has(value T) bool` (`utils/set.go:8-11`)
- `Add(value T) bool` (`utils/set.go:13-24`) — lazily initializes `Map` on first use (nil-map-safe), returns
  whether the value was newly added (false if already present).
- `Remove(value T) bool` (`utils/set.go:26-36`) — nil-map-safe (`Map == nil` short-circuits to `false`),
  returns whether something was actually removed.
- No `New()`/constructor — zero-value `Set[T]{}` is usable directly (`Add` lazily allocates).
- `Size` is a plain public field, hand-maintained by `Add`/`Remove` rather than computed from `len(Map)` —
  callers could desync it by mutating `Map` directly, though nothing in this codebase appears to do so.

`MapSet[K, V]` (`utils/set.go:40-93`) is a map of key → `*Set[V]`, i.e. a multimap/set-of-sets:
- `NewMapSet[K, V]() *MapSet[K, V]` (`utils/set.go:45-50`) — **this one does have an explicit constructor**
  (inconsistent with `Set[T]` itself, which doesn't — minor API asymmetry).
- `SizeForKey(key K) uint`, `Add(key K, value V) bool`, `Has(key K, value V) bool`,
  `RemoveKey(key K) bool`, `RemoveValue(key K, value V) bool` (`utils/set.go:52-93`).

Both types are genuinely used elsewhere in the tree (confirmed by grep, not left as a guess):
- `utils.Set[string]` — struct field `Channels` on a websocket session (`websocket-session.go:145`), the
  `Exclusions` field of the query AST's "star selector" node (`query/ast.go:78`, `query/parse2.go:409`,
  and the duplicate `rel/` copies `rel/ast.go:16`, `rel/from_json_select.go:66,109`), and a local
  `schemas_set` used while building the root query scope (`query/scope.go:209`).
- `utils.MapSet[string, *SwWebsocketSession]` — tracks which websocket sessions are listening to which
  channel/topic (`websocket.go:78`, constructed via `utils.NewMapSet(...)` at `websocket.go:97`).

So `Set`/`MapSet` are load-bearing in the query AST (exclusion lists for `SELECT *`-style selectors) and in
the websocket pub/sub fan-out (which sessions are subscribed to which channel) — not incidental utility
code.

## 6. Container / deployment model

### `Dockerfile` (10 lines of real content, `Dockerfile:1-27`)

Two-stage build:
1. **`setup` stage**, `FROM busybox` (`Dockerfile:3-5`): creates `/app/secrets` and `/app/saml` and
   `chown`s them to uid/gid `1000:1000`. Purpose: pre-create writable, correctly-owned mount points, since
   the final image is `scratch` and has no shell/tools to `mkdir`/`chown` at runtime.
2. **Final stage**, `FROM scratch` (`Dockerfile:8`) — an empty base image, no OS, no shell, no libc at all.
   A commented-out `FROM alpine:3.12` (`Dockerfile:7`) shows an earlier iteration used Alpine but the
   project moved to `scratch`.
   - `EXPOSE 3001` (`Dockerfile:11`) — matches the hardcoded `srv.Port` in `main.go:107`.
   - Copies the pre-owned `/app` tree from the `setup` stage (`Dockerfile:14`).
   - `ADD server /server/server` (`Dockerfile:16`) — the statically-linked Go binary (built by `make
     server`, see below) is the **entire application payload**.
   - `ADD ca-certificates.crt /etc/ssl/certs/ca-certificates.crt` (`Dockerfile:17`) — since `scratch` has
     no OS-provided CA bundle, this file (185 KB, present at repo root but **git-ignored** — `.gitignore:4`
     has `*.crt` — confirmed not tracked via `git ls-files`/`git check-ignore`) is manually bundled so the
     Go binary's outbound TLS (OAuth providers, SAML IdPs, etc.) can verify server certificates. It is
     purely a local **build artifact**, produced by `make image` copying the host's live
     `/etc/ssl/certs/ca-certificates.crt` into the repo root just before `docker build` (`Makefile:43`) —
     see §7/§8. This means the `Dockerfile` is not buildable from a clean checkout without first running
     `make image` (or otherwise placing a `ca-certificates.crt` at the repo root) — `docker build .` alone
     will fail with a missing-file error.
   - `WORKDIR /app`, `USER 1000:1000` (non-root) (`Dockerfile:19,21`).
   - `VOLUME ["/app/saml", "/app/secrets"]` (`Dockerfile:24`) — these two paths are meant to be
     bind-mounted/persisted externally.
   - `ENTRYPOINT ["/server/server"]`, `CMD []` (`Dockerfile:25-26`).

**Important discrepancy**: the Dockerfile's `ENTRYPOINT` runs the Go binary **directly** — there is no
s6-overlay, no PostgREST, and no supervisor process anywhere in this `Dockerfile`. The `root/` directory
(s6 service scripts, `config.sh`) and the described "container supervises Go server + PostgREST together"
model does **not** correspond to what this `Dockerfile` actually builds. This is not just an inference from
absence — `root/config.sh` is internally self-contradictory as a script for any *single* base image: its
shebang is `#!/bin/ash` (the shell used by BusyBox/Alpine), it calls `install_packages` (`root/config.sh:15`,
a helper specific to Bitnami's `minideb`/Debian-slim images, not present on Alpine or plain Debian), and it
finishes with `apt remove`/`apt autoremove` (`root/config.sh:28-29`, Debian/Ubuntu's package manager — Alpine
uses `apk`, not `apt`, so a genuine Alpine image couldn't run these lines at all). No single base image
satisfies `ash` + `install_packages` + `apt` simultaneously, which means `config.sh` itself is already a
patchwork across at least two different base-image generations, independent of whether it ever matched the
`Dockerfile`. (The repo has no commit history to date — `git log` on `main` returns no commits — so it
isn't possible to confirm via history whether `root/` predates or postdates the current `scratch`-based
Dockerfile; treat it as **a retired or parallel deployment mode**, not the one that actually ships today,
based on the internal evidence above rather than history.) This should be called out clearly when
redesigning deployment for the new server — it is a real inconsistency worth resolving rather than carrying
forward silently.

### `root/config.sh` (`root/config.sh:1-30`) — image provisioning script for the (retired/alternate) s6 image

An `ash` script (Alpine shell) intended to run at image-build time:
- Pins `S6_VERSION="v1.18.1.3"` and `POSTGREST_VERSION="v12.2.8"` (`root/config.sh:3-4`) — note this
  **disagrees** with the `Makefile`'s own `POSTGREST_VERSION = v12.2.3` (`Makefile:1`) — two different
  PostgREST versions pinned in two different places, another inconsistency to clean up.
- Creates a non-root user `user` (uid 1000, no home dir creation, shell `/usr/bin/bash`) (`root/config.sh:9`).
- Creates `/static`, `/protected`, `/saml` (chowned to `user`) (`root/config.sh:10-13`).
- `install_packages libpq5 ca-certificates xz-utils entr wget` (`root/config.sh:15`) — installs the Postgres
  client lib, CA certs, xz (for extracting the PostgREST tarball), `entr` (file-watcher, used by the `srv`
  service script below), and `wget` (used only transiently to fetch things, then removed).
- Downloads and unpacks **s6-overlay** to `/` (`root/config.sh:18-20`).
- Downloads and unpacks a **statically-linked PostgREST** binary to `/usr/local/bin/postgrest`
  (`root/config.sh:23-26`).
- Removes `wget` and autoremoves unneeded packages afterward (`root/config.sh:28-29`) to keep the image
  slim.

### `root/etc/services.d/srv/run` (`root/etc/services.d/srv/run:1-4`) — s6 service: the Go server

```sh
#!/usr/bin/with-contenv sh
echo /server/server | entr -n -r sh -c "cp /server/server /tmp/server && exec su user -c /tmp/server"
```
Under s6-overlay's `/etc/services.d/<name>/run` convention, this would be supervised as a long-running
service. Behavior: uses `entr` to **watch the `/server/server` binary file itself**; whenever it changes on
disk (e.g. a new binary is bind-mounted or overwritten by CI/dev tooling), `entr -r` (restart mode) kills
and relaunches the inner command. The inner command copies the binary to `/tmp/server` first (comment:
"copy to allow for dev to overwrite the server") — this avoids "text file busy" issues when the mounted
binary is replaced while running — then executes it as the unprivileged `user` via `su user -c`. This is a
**live binary-replacement dev loop baked into the container itself**, independent of both the Makefile's
`watch` target and `main.go`'s own `SW_WATCH_SERVER` self-relaunch mechanism (see §7 below) — a third,
Docker-native way of achieving similar "restart on change" behavior.

### `root/etc/services.d/postgrest/run` (`root/etc/services.d/postgrest/run:1-8`) — s6 service: PostgREST

```sh
#!/usr/bin/with-contenv sh
if [ "$PGRST_DB_URI" ]; then
  su user -c '/usr/local/bin/postgrest /etc/postgrest.conf'
else
  echo "No PGRST_DB_URI, not launching postgrest and hanging forever"
  sleep 999999999
fi
```
Conditionally launches PostgREST (as the unprivileged `user`) only if `PGRST_DB_URI` is set; otherwise the
service process just sleeps forever (a no-op placeholder so s6 doesn't treat the service as crashing/
restart-looping when PostgREST isn't wanted in a given deployment).

### `root/etc/postgrest.conf` (`root/etc/postgrest.conf:1-17`)

A PostgREST config file whose values are **all** s6-overlay `with-contenv` style env-var substitutions
(`$(VAR)` syntax) rather than literals — i.e. the actual values are supplied entirely through environment
variables at container start, and this file is just a template:
- `db-uri`, `db-schema`, `db-anon-role`, `db-pool` ← `PGRST_DB_URI`, `PGRST_DB_SCHEMA`, `PGRST_DB_ANON_ROLE`,
  `PGRST_DB_POOL`.
- `server-host`, `server-port` ← `PGRST_SERVER_HOST`, `PGRST_SERVER_PORT`.
- `jwt-secret`, `secret-is-base64`, `role-claim-key` ← `PGRST_JWT_SECRET`, `PGRST_SECRET_IS_BASE64`,
  `PGRST_ROLE_CLAIM_KEY`.
- Commented-out (unused) lines: `server-proxy-uri`, `jwt-aud`, `max-rows`, `pre-request`
  (`root/etc/postgrest.conf:9,12,15-16`) — available knobs not currently wired up but present as documentation
  of what could be enabled.

Overall picture (for the s6/PostgREST image variant, keeping in mind the caveat above that it does not match
today's actual `Dockerfile`): s6-overlay is the process 1 / supervisor; it runs two long-running services
side-by-side — the Go server (via `entr`, self-restarting on binary replacement) and PostgREST (conditional
on `PGRST_DB_URI`) — both de-privileged to a shared `user` (uid 1000) after root-only setup (`config.sh`)
completed at image-build time.

## 7. Build tooling (`Makefile`)

Key variables (`Makefile:1-12`):
- `POSTGREST_VERSION = v12.2.3` (disagrees with `root/config.sh`'s `v12.2.8`, see §6).
- `REGISTRY = eu.gcr.io/divine-arcade-94510`, `REPO = goserver` → images are pushed to a Google Container
  Registry path `eu.gcr.io/divine-arcade-94510/goserver:<TAG>`.
- `TAG` is computed by `grep`-ing the `VERSION` constant out of `version.go` (`Makefile:5`).
- `gofiles` / `webfiles` / `patfiles`: file-glob variables driving Make's dependency tracking, covering Go,
  TypeScript/TSX, HTML, CSS files, plus `.pat` template files and a `web/` frontend tree.
- `PATH` is extended with `web/node_modules/.bin` (`Makefile:12`) so frontend tool binaries are reachable.

Targets:
- **`all: server`** (`Makefile:16`) — default target, just builds the Go binary.
- **`_templates`** (`Makefile:18-20`) — runs a `./patron.ts` script over all `.pat` files to generate
  templates, touches a sentinel file `_templates` for Make's staleness tracking.
- **`web/static/test.js`** (`Makefile:22-23`) — bundles a `web/test.tsx` frontend entry point with `bun
  build` (minified, sourcemapped, CSS-as-text loader) into `web/static/`.
- **`server: $(gofiles) web/static/test.js _templates`** (`Makefile:25-28`) — the main compile target:
  ```
  GOAMD64=v2 GOOS=linux CGO_ENABLED=0 go build -ldflags="-s -w"
  ```
  - `CGO_ENABLED=0` → a fully static Go binary with **no libc dependency at all** (not even musl) — this is
    what makes running it in a `FROM scratch` container possible with zero runtime libraries. Note this
    directly contradicts the README's musl/Alpine framing (see Notes below) — the actual build doesn't link
    against musl, it avoids libc entirely via pure-Go networking/DNS.
  - `-ldflags="-s -w"` strips debug symbols and the DWARF table (smaller binary, no debugger symbols) — but
    does **not** inject any version string via `-X`; `VERSION` remains the hardcoded Go constant (§4).
  - `GOAMD64=v2` targets a slightly newer x86-64 microarchitecture baseline (post-2013 CPUs) for a modest
    codegen improvement.
  - Commented-out static-analysis steps `nilaway` and `staticcheck` (`Makefile:26-27`) — available but
    disabled/not enforced in the build.
- **`watch`** (`Makefile:30-31`):
  ```
  while inotifywait -qre close_write . ; do make server --no-print-directory ; done
  ```
  This is **not** `entr`-based (despite the README describing `entr` as the tool used for `make watch` —
  README: *"L'utilitaire `entr` ... Utilisé pour le `make watch` du serveur"*). The actual implementation
  uses `inotifywait` (from `inotify-tools`) in a shell loop, recompiling on any `close_write` event anywhere
  under the current directory. **The README is out of date relative to the Makefile** — `entr` is a listed
  prerequisite and is genuinely used, but inside the *container* (`root/etc/services.d/srv/run`, §6), not by
  `make watch` on the host. Worth correcting in any new docs/README for the rewrite.
- **`test-cleanup`** (`Makefile:36-37`) — force-removes any leftover Docker containers labeled
  `com.sales-way.test` before a test run.
- **`test: test-cleanup`** (`Makefile:33-34`) — runs `bun test --bail=1` inside `test/`.
- **`reset`** (`Makefile:39-40`) — deletes the compiled `server` binary.
- **`image: server`** (`Makefile:42-44`) — copies the *host's* live `/etc/ssl/certs/ca-certificates.crt`
  into the repo root (overwriting the checked-in `ca-certificates.crt`!) then runs
  `docker build --no-cache --rm -t "$(IMAGE_DST)" .`. Note `--no-cache` — every image build is from scratch,
  no Docker layer caching, presumably to guarantee reproducibility/freshness at the cost of build time.
- **`upload: image`** (`Makefile:46-48`) — `docker push $(IMAGE_DST)` (a commented-out `#gcloud docker -a`
  hints at a formerly-needed GCR auth step, now presumably handled by `gcloud auth configure-docker` outside
  the Makefile).

### Two competing "restart on change" mechanisms

There are, in fact, **three** distinct auto-restart-on-change mechanisms in this codebase, not just two:

1. **`make watch`** (`Makefile:30-31`) — host-side, `inotifywait`-driven, re-invokes `go build` (i.e.
   recompiles) whenever any tracked file changes. Pure build-loop, no process supervision of the running
   binary itself.
2. **Container `entr` restart** (`root/etc/services.d/srv/run`, §6) — Docker-side (in the retired/alternate
   s6 image), watches the **binary file** `/server/server` and kills+relaunches the process when the file
   changes (e.g. because `make watch` on the host, via a bind mount, just replaced it).
3. **`main.go`'s `SW_WATCH_SERVER` / fsnotify self-relaunch** — gated by `SW_WATCH_SERVER` env var (or
   `SW_ENABLE_DEBUG=true`) at `main.go:318-320`, spawns `watchAndRelaunch` (`main.go:346-383`) which uses
   `fsnotify` to watch **its own executable path** (`os.Executable()`) and, on a filesystem event,
   `syscall.Exec`s itself again in-place (replacing the process image, same PID) after a 200ms debounce
   sleep.

Mechanisms 2 and 3 are **functionally redundant** — both watch the same binary file and both restart the
process when it changes, just via different primitives (`entr`'s external kill/relaunch vs. the process's
own `fsnotify` + `syscall.Exec` self-replacement) and in different contexts (mechanism 2 assumes the s6/
container image, which per §6 doesn't match the actual current `Dockerfile`; mechanism 3 works regardless of
how the binary is launched, including bare-metal/dev). If mechanism 3 is enabled inside a container that
*also* runs mechanism 2's `entr` wrapper, you'd get double restart logic on a single file change (though in
practice this scenario can't currently occur end-to-end since the shipping `Dockerfile` doesn't include the
s6 service scripts at all — see §6). For the rewrite: pick exactly one restart-on-change strategy.

## 8. Notes / things to reconsider for the rewrite

- **`Dockerfile` vs. `root/` mismatch (§6)**: the shipping `Dockerfile` runs the Go binary directly from
  `scratch` with no supervisor and no PostgREST in the same container. The `root/` tree (s6-overlay,
  `config.sh`, two service scripts, `postgrest.conf`) describes a materially different, Alpine/Debian-based,
  s6-supervised, dual-process container that isn't actually built by anything in this repo. Resolve which
  model is real before carrying either forward.
- **Two different PostgREST version pins** (`Makefile:1` says `v12.2.3`, `root/config.sh:4` says
  `v12.2.8`) — symptomatic of the same drift between the two deployment descriptions.
- **`VERSION` is a hand-edited constant, not build-stamped.** No `-ldflags -X` injection, no `git describe`.
  The Makefile scrapes the constant back out via `grep` for image tagging, which is fragile (depends on
  exact source formatting) and means version bumps require a source-code edit + commit rather than being
  derivable from git tags/CI.
- **Client-version compatibility checking is unimplemented, only scaffolded** (§4). `getClientVersion`
  reads a version marker out of the compiled frontend bundle and logs it, but nothing compares it to
  anything or gates behavior on it. If backend/frontend compat enforcement is a real requirement, it needs
  to actually be built for the new server; if not, drop the scaffolding.
- **Stack traces leak to clients unconditionally in `_printStackTrace`** (`stacktrace.go`, §3), regardless
  of `DEBUG_ENABLED` — only the raw SQL text is gated by that flag, not the file/function/line stack trace
  table. This is inconsistent with `ReplyError`/`RecovererColored`, which never leak stack traces to the
  client. Recommend gating the entire stack-trace table behind a debug/dev flag (or removing it from
  production responses) in the rewrite.
- **Two parallel logging APIs** (`SwServer.LogInfo/LogWarn/LogError` methods vs. free functions of the same
  names in `sw/general.go`) with different glyphs/colors and no clear division of responsibility — pick one
  in the rewrite.
- **`LogError` and `LogWarn` on `SwServer` render the identical `"⚠ "` glyph**, differing only by color
  (`sw/defs.go:50-58`) — easy to misread in a non-colorized terminal/log aggregator (e.g. when stdout is
  captured to a file or shipped to a log pipeline that strips ANSI codes, warnings and errors become
  visually indistinguishable). Consider distinct glyphs regardless of color.
- **Plugin system is fully implemented but has zero real-world plugins in this repo** (§2) — worth deciding
  whether the new server keeps native Go `.so` plugins (Linux-only, requires matching Go toolchain/ABI
  between host and plugin build, generally considered fragile/discouraged in modern Go) or replaces it with
  something else (subprocess/RPC plugins, compile-time registration, etc.) for the rewrite.
- **`registerPlugins` uses `plugin.Open`, which requires `CGO_ENABLED=1`** on most platforms/Go versions for
  the *plugin* itself to be buildable as a `.so`, yet the main server binary is built with
  `CGO_ENABLED=0` (`Makefile:28`). The main binary can still load a plugin built elsewhere with CGO enabled
  (loading doesn't require cgo, only building `.so` plugins does), but this asymmetry is easy to trip over
  and worth documenting explicitly if the plugin system is kept.
- **`make image` mutates the repo root** by copying the host's live CA bundle over the checked-in
  `ca-certificates.crt` (`Makefile:43`) before `docker build` — i.e. the checked-in file is a build artifact
  that gets silently overwritten by whichever machine happens to run `make image` last. Consider making this
  explicit (e.g. write to a build/ dir, or document that the checked-in file is disposable) in the rewrite's
  tooling.
- **README (`README.md`) describes `entr` as the mechanism behind `make watch`**, but the actual `Makefile`
  target uses `inotifywait` (§7) — `entr` is real but used only inside the container's `srv` service script.
  The README also documents French-language TODOs (Vault integration for secrets, SIEM logging integration,
  dynamic OAuth/SAML endpoint reconfiguration without restart) that remain open in this legacy code and may
  be worth carrying into the new design doc as feature requirements rather than doc-only TODOs.
- **`utils.Set[T]` has no constructor** while its sibling `utils.MapSet[K,V]` does (`NewMapSet`) — minor API
  inconsistency; harmless since `Set[T]{}` zero-value works, but worth normalizing in a rewrite's stdlib-ish
  utility package.
- **`sw/defs.go:115-117`** contains a trailing comment listing three methods (`GetEnvOrDefault`,
  `SetRoleCookie`, `UnsetCookie`) that do not exist on `SwServer` — either stale planning notes or evidence
  that those responsibilities were implemented elsewhere as free functions instead; resolve one way or the
  other rather than carrying the ambiguity forward.
- **`ca-certificates.crt`** (185 KB, repo root, not read for content per task scope) is the CA bundle
  `ADD`ed into the `scratch` image at `/etc/ssl/certs/ca-certificates.crt` (`Dockerfile:17`) so the Go
  binary can validate TLS certificates for outbound calls (OAuth providers like Google/Salesforce, SAML
  IdPs). It is **git-ignored** (`*.crt` in `.gitignore:4`, confirmed untracked), deliberately generated
  as a build artifact by `make image` copying the host's own CA bundle (`Makefile:43`) immediately before
  `docker build`. This is arguably the *cleaner* half of the design (no stale CA bundle committed to git),
  but it does mean the `Dockerfile` cannot be built standalone/reproducibly outside of `make image` — a
  fresh clone plus a bare `docker build .` will fail. Worth making this dependency explicit (e.g. a
  `Dockerfile` build stage that fetches/generates the CA bundle itself, or clear tooling docs) in the
  rewrite rather than relying on `make image`'s ordering.
