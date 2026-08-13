# Legacy Server — Overview & Index

This directory is a working reference on `_legacy/` (Go module `sales-way.com/server`), written to support designing the new server at this repo's root (Go module `github.com/ceymard/rel`). It is deliberately not prose documentation of the legacy product — it is an exhaustive, file:line-cited account of what the legacy code actually does, gaps and bugs included, so design decisions for the new server can be made with full knowledge of what worked, what was aspirational, and what quietly doesn't work.

Each doc below was produced by an independent close reading of the corresponding source files, with an adversarial "verify against the code, don't repeat the README" pass. Read the individual docs for detail; this file is the map and the cross-cutting synthesis.

## 1. What the legacy system actually is

**A single Go binary, full stop — PostgREST is no longer part of this
version.** `_legacy/Dockerfile` builds a `scratch`-based image containing
only the statically-linked Go server (one `ENTRYPOINT`, no supervisor, no
second process). The `root/` directory (s6-overlay service scripts,
`config.sh`, `postgrest.conf`) describes an **older, retired deployment
generation** that *did* run the Go server and PostgREST side by side under
s6 — it is not wired into today's `Dockerfile` or `Makefile`, and nothing in
the Go code execs, proxies to, or configures a `postgrest` process. The
`PGRST_*`-prefixed env vars the Go code still reads (`PGRST_DB_URI`,
`PGRST_DB_SCHEMA`, `PGRST_DB_ANON_ROLE`, `PGRST_JWT_SECRET`, ...) are
inherited **variable-name aliases** kept for config-compatibility with
older deployments, not evidence of a live PostgREST sidecar. See
[`postgres-integration.md`](postgres-integration.md) §1 (corrected) and
[`infra-plugins-misc.md`](infra-plugins-misc.md) §6 for the full evidence,
including internal inconsistencies in `root/config.sh` that suggest it was
already a patchwork across base-image generations independent of this.

Practically, this means the Go server's custom query/write engine (`/rel`,
documented in [`query-language.md`](query-language.md)) isn't "a hand-rolled
competitor running next to PostgREST" for this version — it **is** the
REST-over-Postgres API. There is no other backend process to route
application traffic to. An nginx (or similar) reverse proxy in front is
still presumably expected for TLS termination and `X-Accel-Redirect` file
downloads, but only one application process sits behind it now.

The Go binary's own responsibilities, as actually wired into `main()`:

- **Authentication** for the whole system (password, OAuth, SAML), unified behind one JWT cookie — [`auth.md`](auth.md), [`oauth.md`](oauth.md), [`saml.md`](saml.md).
- **Two, non-interoperating generations of a hand-rolled data API** — the live, working REST-over-Postgres surface (`query`) and an unfinished JSON-based successor (`rel`) — [`query-language.md`](query-language.md) and [`rel-package.md`](rel-package.md), reconciled in §2 below.
- **Postgres schema introspection**, cached in memory and reloaded on demand, that both data-API generations and TypeScript codegen depend on — [`postgres-integration.md`](postgres-integration.md).
- **A websocket endpoint** for LISTEN/NOTIFY-driven live updates — [`websockets.md`](websockets.md).
- **SQL migrations** ("dmut"), run at startup and on `SIGUSR1` — [`dmut-migrations.md`](dmut-migrations.md).
- **Static file / SPA serving, a plugin loader, panic recovery, and Docker/build tooling** — [`infra-plugins-misc.md`](infra-plugins-misc.md).
- **A minimal Bun-based test/demo web console and integration test suite**, not a real product frontend — [`web-frontend.md`](web-frontend.md).

The shared "app context" threading through nearly every file is `sw.SwServer` (`sw/defs.go`): `Router`, `Pool`, `Hostnames`, `Port` (hardcoded `"3001"`, no env override), `Schemas`, `Tables`, `Functions`, `DmutStatus`, `RootScope`, `AvailableLoginFunctions`, plus `LogInfo`/`LogWarn`/`LogError`, `HasPg`, `PgSimpleExec`/`Query`/`QueryRow`, `PgReloadTables`. Full field/method inventory in [`infra-plugins-misc.md`](infra-plugins-misc.md) §1 — worth reading first, since every other doc assumes familiarity with this struct.

## 2. The most important finding: two data-API generations, only one of which works

This matters directly for the new project because **the new module is literally named `rel`**, matching the legacy `rel` sub-package — strong evidence the new server is meant to continue from that package's design rather than `query`'s.

| | `query` package | `rel` package |
|---|---|---|
| Route | `/rel`, `/rel/*` (`pg2.go`) | `/rel2` (`pg3.go`) |
| Input | Hand-written string DSL (Pratt parser) | JSON body (closed schema, no lexer) |
| Pipeline | Parse → resolve against schema → **compile to SQL** (`json_agg`/`row_to_json` correlated subqueries) → execute → return JSON | Parse → resolve against schema → flatten write payload to `(id, parentId, tableId, json)` tuples → **stop** |
| Executes SQL? | **Yes** — this is the live, working data API | **No** — `/rel2`'s handler only `pp.Println`s diagnostics; no SQL is ever generated or run |
| Test status | No dedicated test file found | One test exists, and **fails as committed** (two JSON key-name typos vs. the parser) — locally verified fixable, confirming the implementation is more correct than its own test |

In short: `query` is the thing actually serving traffic today; `rel` is a live-but-diagnostic-only spike toward a JSON-driven successor that stops exactly at "resolved AST + flattened write rows," with no SQL generation or write execution ever implemented. Both share `pg/relationships.go`'s FK-derived relationship metadata and `query`'s expression AST/algebra, but `rel` reimplements its own (near-duplicate) resolve context rather than reusing `query`'s.

**For the new server**, [`rel-package.md`](rel-package.md) §9 gives a detailed verdict: the closed-JSON-schema parsing discipline, FK-verified join resolution, and write-mode inheritance model are worth carrying forward; the "how resolved reads become SQL" question should be studied from `query/sql_constructs.go` (the part that actually works today), and "how resolved+flattened writes become real multi-table SQL in dependency order" has no prior art in either package — it needs designing from scratch. Also relevant: `templates/json_flat.pat` (documented in [`web-frontend.md`](web-frontend.md) §6) is a separate, already-working recursive CTE generator for nested insert/upsert/merge/delete that solves a similar problem to `rel`'s unfinished write path — worth comparing before designing a new one.

## 3. Authentication converges from three sources into one mechanism

Password login, OAuth (`goth`-based: Google, Salesforce, a hand-rolled Yahoo provider), and SAML (`crewjam/saml`) all terminate in the same two calls: `auth.external_login(text)`/`auth.login(text,text)` (Postgres functions, introspected at startup/reload — [`auth.md`](auth.md) §4) and `jwtSetCookieForRole` (mints the session cookie). This means the new server's session/cookie design only needs to be built once and can stay decoupled from however many identity sources feed it — the legacy design already validates that pattern. See [`auth.md`](auth.md), [`oauth.md`](oauth.md), [`saml.md`](saml.md) for exact endpoints, env vars, and per-mechanism flow.

Impersonation (`SW_IMPERSONATOR_ROLE`, `/auth/impersonate/{user}`) is a real feature with a real risk documented precisely in [`auth.md`](auth.md) §6 — not just the README's generic warning, but the specific code-level reason it's dangerous (privilege propagates without re-checking DB membership per hop; target username reaches raw SQL; it's a state-changing `GET`).

## 4. Cross-cutting punch list of concrete defects found

These surfaced from actually running tests, tracing call sites, and cross-checking doc claims against code — not from speculation. Each is cited with file:line in its source doc; this is a flat index so nothing gets lost between the nine docs.

- **PostgREST is not part of this deployment, despite the Dockerfile-adjacent `root/` tree and lingering `PGRST_*` env var names suggesting otherwise** — confirmed directly against `_legacy/Dockerfile` (a `scratch` image with a single `ENTRYPOINT`, no supervisor, no PostgREST binary). `root/` describes an older, retired s6-based deployment mode that isn't referenced by the current `Dockerfile`/`Makefile`. See §1 above and [`postgres-integration.md`](postgres-integration.md) §1.
- **JWT session never actually refreshes** — `jwtRefreshCookieMiddleware` slides the cookie's `Expires` on every request but never re-signs the JWT, so the JWT's own absolute `exp` still forces a hard logout regardless of activity. ([`auth.md`](auth.md))
- **Two env vars are dead by construction** — `SW_WEBSOCKETS_DISABLE` and `SW_WEBSOCKETS_LOG` (and the same pattern in `plugins.go`) are read via `getenvOrDefault(name)` with a single argument, so the function's own "last arg is the literal default" contract makes the call return that literal immediately — `os.Getenv` is never reached. Websockets are unconditionally enabled and debug logging unconditionally on, regardless of these vars' values. ([`websockets.md`](websockets.md))
- **Plain INSERT can silently trigger destructive DELETEs on child relationships** — `templates/json_flat.pat`'s generated CTEs have asymmetric hardcoded recursion, so `OP_INSERT` still emits a `DELETE` CTE for nested relations. ([`postgres-integration.md`](postgres-integration.md))
- **A failed schema reload nils out the entire previously-cached schema** instead of keeping the last-known-good copy, and the `/heartbeat` endpoint reports DB status as `"ok"` whenever a pool exists, independent of whether it's actually reachable. ([`postgres-integration.md`](postgres-integration.md))
- **An unrecovered panic path reachable via `SIGUSR1`**: dmut v1's `COMMENT ON` statements have no `Down()` implementation and panic on rollback; the signal-handler goroutine that triggers migrations has no `recover()` (only HTTP handlers get that, via `RecovererColored`). ([`dmut-migrations.md`](dmut-migrations.md))
- **Both dmut v1 and v2 are imported**; which one runs is decided implicitly by whether `/dmut2` or `/dmut/index.dmut` exists on disk, not by an explicit switch. ([`dmut-migrations.md`](dmut-migrations.md))
- **Stack traces leak to clients unconditionally** on panic (`error_pages.go`/`stacktrace.go`) — only SQL text in error output is gated behind `SW_ENABLE_DEBUG`. ([`infra-plugins-misc.md`](infra-plugins-misc.md))
- **Client-version compatibility check is dead scaffolding** — `getClientVersion` reads a marker but the result is never compared against anything. ([`infra-plugins-misc.md`](infra-plugins-misc.md))
- **A likely pool-misuse bug**: `PgSimpleQuery`/`PgSimpleQueryRow` release the connection back to the pool before the caller reads the result. ([`infra-plugins-misc.md`](infra-plugins-misc.md))
- **`query` package parser gaps**: `in`/`like`/`ilike`/`between`/`similar`/`isnull`/`notnull` are tokenized and given precedence but have no case in the infix parser switch, so they don't actually parse despite looking supported; the HTTP dispatch in `pg2.go` has no case for `OP_UPDATE`, so a plain `update` operation silently does nothing. ([`query-language.md`](query-language.md))
- **SAML has one hardcoded, shared, non-rotating self-signed SP certificate** at a fixed filesystem path, `AllowIDPInitiated: true` hardcoded with a `FIXME` in the source, and no working Single Logout despite an advertised SLO endpoint. ([`saml.md`](saml.md))
- **OAuth's CSRF/session-store secret (`SESSION_SECRET`) is required by the underlying `gothic` library but is completely undocumented** in the README next to the provider key/secret vars it does document. ([`oauth.md`](oauth.md))
- **The websocket subsystem doesn't do what its own header comment advertises**: `main.go`'s top comment promises "several [queries] in parallel" over one socket, but processing is strictly serial per connection (single read-loop goroutine); LISTEN/NOTIFY *is* fully implemented, but "filters" means regex-matched channel-name SQL hooks, not per-message payload filtering, and the one dedicated LISTEN connection has no reconnect logic. ([`websockets.md`](websockets.md))

None of these are reasons to distrust the docs — they're exactly the kind of thing this exercise was for. Treat this list as a "do not silently re-implement" checklist when translating legacy behavior into the new server's design.

## 5. Reading order suggestion

1. [`infra-plugins-misc.md`](infra-plugins-misc.md) — establishes `SwServer` and the deployment model everything else assumes.
2. [`auth.md`](auth.md), then [`oauth.md`](oauth.md) and [`saml.md`](saml.md) — the unified session model and its three feeders.
3. [`postgres-integration.md`](postgres-integration.md) — schema introspection and connection handling (and why PostgREST, despite lingering config naming, isn't actually part of this deployment).
4. [`query-language.md`](query-language.md) then [`rel-package.md`](rel-package.md) — the working data API, then its unfinished successor (read in this order; `rel-package.md` repeatedly contrasts itself against `query`).
5. [`websockets.md`](websockets.md) and [`dmut-migrations.md`](dmut-migrations.md) — the two remaining live subsystems.
6. [`web-frontend.md`](web-frontend.md) — lowest priority for backend design purposes, but contains the `.pat` template mechanism and `parser_prompt.txt`'s meta-context on how the query parser/templates were originally developed.

## 6. Files in this directory

| File | Subsystem |
|---|---|
| `auth.md` | Password login, JWT/cookie session, impersonation |
| `oauth.md` | OAuth (goth): Google, Salesforce, Yahoo |
| `saml.md` | SAML SSO (crewjam/saml) |
| `postgres-integration.md` | DB connection, schema introspection, why PostgREST is no longer used, TS codegen |
| `query-language.md` | The live `/rel` string-DSL query/write engine |
| `rel-package.md` | The `/rel2` JSON-based query/write prototype (ancestor of this project) |
| `websockets.md` | `/ws` endpoint, LISTEN/NOTIFY pub-sub protocol |
| `dmut-migrations.md` | SQL migration runner (v1/v2), startup and `SIGUSR1` reload flow |
| `infra-plugins-misc.md` | `SwServer`, plugins, panic recovery, versioning, Docker/Makefile |
| `web-frontend.md` | Bun-based test console, `.pat` templates, integration test suite |
