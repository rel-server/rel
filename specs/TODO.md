# Spec completeness TODO

What's still missing before the specs in this directory are complete enough to start
implementing, measured against the feature list in `00-general.md` (the stated intent).
Grouped by how much they block implementation, not by file.

## Blocking / foundational — nothing downstream can be built confidently without these

- **Introspection lifecycle** (`01-introspection.md`, now written up). The mechanism
  itself exists and is tested (`pg/`), and its contract is now documented. The `SIGUSR1`
  reload sharp edge (swapping the schema cache without racing requests already in flight)
  is now resolved — see the `dmut` / migrations bullet below, `03-dmut.md`, and the `boot`
  package.
- **Query compiler architecture.** `querying.md` specifies the Reading/Writing algorithms
  as SQL shape and recursion rules, not as Go types. Pass 1 (tree inflation + DB
  resolution) is now implemented and tested (`query/node_parse.go`, `query/node_resolve.go`,
  `pg/info_searchpath.go`, `pg/info_lookup.go`) : JSON decode, relation/function
  resolution (search path + blacklist), `ResolveJoin` per join, write_mode
  defaulting/validation, `on_conflict`/insert/update-column resolution, `pg.query.maxdepth`
  enforcement. Pass 2 (expression resolution : scope, blacklist, shape, writability) is
  now also implemented and tested (`query/expression_resolve.go`, `query/shape.go`,
  `query/scope.go`) — identifier/`.`-chain resolution, `call`/`agg` catalog resolution,
  writability with the extractor, including chaining into a computed/renamed key exported
  by a child's own `select` at any nesting depth (unified into one recursive mechanism,
  no remaining gap here). Pass 3 (Reading Algorithm SQL codegen, `query/sql.go`/
  `query/sql_expr.go`) and pass 4 (Writing Algorithm, `query/write.go`/
  `query/write_denormalize.go`/`query/write_dml.go`) are now both implemented and tested
  against real Postgres — the full per-node recursive SELECT compilation including the
  LATERAL-sharing exception, and phased INSERT/UPDATE/UPSERT/DELETE with per-node key
  recovery across all seven `write_mode` values. Both are scoped to codegen+execution
  only ; see the connection-pool/lifecycle item directly below for what's still missing
  before either can run inside a real request. `writer/` (the target-agnostic
  text-building writer, plus a Postgres-specific `SQLWriter` wrapper for
  bind-params/identifier-escaping) underpins both passes.
  Two engine gaps found while building read-path benchmarks (`query_bench/`) were since
  fixed : a row-type-taking computed column (`querying.md ## Scoping`'s `alias.func_name`/
  `func_name(alias)` paragraph) called with the relation's own self-alias as its bare
  argument now compiles (`query/sql_expr.go`'s `compileResolvedField` recognizes a
  self-reference and emits its already-tracked SQL alias — a genuinely different node's
  alias embedded as a bare value, e.g. a child/sibling, stays unsupported, that's a
  materially harder LATERAL/subquery-correlation problem) ; and a `RETURNS TABLE(...)`
  function's own output columns now resolve (`pg.Function.RecordRelation`, built at
  introspection time directly from the function's OUT/TABLE-mode arguments, since every
  record-returning function shares one generic `pg_catalog.record` prorettype with no
  backing composite type for `Type.Relation` to ever resolve). Structurally verified
  (not just asserted) that a `RETURNS TABLE` function still can never be the CHILD/
  joined-into side of any relationship — Postgres can't index a function's computed
  output, and `### Join eligibility` requires exactly that on the child side — only a
  query root or the parent/outer side of an outgoing join out to a real indexed relation.
  Benchmarks now exist for both the write path (`query/write_bench_test.go` : flat
  insert, outgoing FK chains of varying depth/fan-out, batch-size scaling) and the read
  path (`query_bench/`, against the richer hotel/booking fixture rather than movie/
  director, for a realistic volume/shape signal) — meant as a baseline for catching
  future performance regressions, not a one-off measurement.
- **Connection pool / transaction lifecycle.** Never given its own spec, but several other
  documents assume it exists : `SET LOCAL ROLE` timing (`jwt-roles-and-http.md`), the
  commit-before-select response design (`querying.md ## Response Shape`), and `_data`
  being a session-scoped temp table that must be guaranteed clean across pooled
  connection reuse. **Resolved for the current `POST /rel` vertical slice**
  (`server/rel.go`) : one connection is acquired from the pool per request and pinned for
  its whole lifetime (`db.Pool.Acquire`, `defer conn.Release()`) ; `_data` is
  `create temp table if not exists` at acquire time, immediately followed by an explicit
  `truncate _data` so this request's correctness never depends on the *previous*
  request's own end-of-request cleanup having actually run (killed process, swallowed
  error) ; a second `truncate _data` runs at the very end via `defer`, against a fresh
  `context.Background()` so a client disconnect/cancelled request doesn't skip it. Still
  open : whether a read-only `Sequence` (no write item at all) should also share one
  transaction across its queries — right now it doesn't, see the note added to
  `querying.md ## Transactions`.
  **`SET LOCAL ROLE` / auth timing — now resolved for `/rel`, one deliberate divergence
  from `jwt-roles-and-http.md`'s literal wording** : `/rel` uses session-scoped
  `SET ROLE`/`RESET ROLE` on the pinned connection, NOT the transaction-scoped
  `SET LOCAL ROLE` `jwt-roles-and-http.md:43` names unconditionally — `/rel` commits its
  write transaction and then runs every item's read query AFTER that commit, still on the
  same connection, so a transaction-scoped role would revert before the reads that also
  need it ever ran. `/rpc` (`rpc/handler.go`) still uses `SET LOCAL ROLE` as spec'd, since
  its whole request (check_session, role switch, the route function call) shares one
  transaction start-to-finish. `_data` is granted to `PUBLIC` every request (it's owned by
  the connecting role, not the switched-to one) so a non-owner role can still write to it.
  Also : `jwt-roles-and-http.md:90`'s "all four lifecycle steps are ordinary
  `func(http.Handler) http.Handler` middleware" isn't fully achievable — only Verify/Renew
  (`jwt/middleware.go`) need no DB and can be genuine middleware ; Check
  (`check_session`) and Apply role both need the request's own connection, which doesn't
  exist yet when generic middleware runs, so those two stay each handler's own
  responsibility (`server/rel.go`'s `applyRole`, `rpc/handler.go`'s inline equivalent),
  sharing the DB-facing half via `dbauth`.
  **Undocumented deployment prerequisite, found empirically (`SET ROLE` under a plain
  `login` role, not a superuser)** : `SET ROLE`/`SET LOCAL ROLE` only succeeds if the
  connecting role is a MEMBER of the target role — every local/testcontainer run so far
  has connected as a superuser (`postgres`), which can `SET ROLE` to anything, silently
  masking this. In production the connecting role is `pg.query.user`, deliberately NOT a
  superuser — so `pg.query.user` must be granted membership in `pg.query.anonymous_role`
  AND every role any JWT in this deployment may carry (`grant "~anonymous" to query_user;`,
  one `grant` per such role), for both `/rel` and `/rpc`, or every anonymous/authenticated
  request 500s with "permission denied to set role" the moment it's deployed against a
  properly-locked-down (non-superuser) connecting role. PostgREST documents the identical
  prerequisite for its own `authenticator`/`web_anon` pattern ; neither `querying.md` nor
  `jwt-roles-and-http.md` states it yet for rel.
  **A second, more serious bug found the same way, since fixed** : introspection itself
  (`pg.NewInfos`) failed outright under a non-superuser connecting role — before `SET
  ROLE` is ever attempted, so the grant above alone does NOT make a non-superuser
  deployment work. Root cause : `INFO_QUERY_CONSTRAINTS` reads `pg_constraint` directly
  (world-readable, unfiltered by design), but the relation map it resolves constraints
  against is built from `information_schema.columns`, which — unlike `pg_constraint` —
  DOES filter by the connecting role's own privileges. Several `pg_catalog` system tables
  (`pg_authid`, `pg_subscription`, `pg_replication_origin`, ...) have real `p`/`u`/`f`
  constraints in `pg_constraint` but are invisible via `information_schema` to anything
  but a superuser, so `FillConstraintInformations` (`pg/info_constraint.go`) hit its own
  "this should not happen" hard-fail on every correctly-locked-down deployment, not just
  a contrived one. Fixed by skipping (not failing on) a constraint whose owning or target
  relation the connecting role can't see — the same pattern `info_index.go` already used
  for the identical class of mismatch — since a relation the connecting role can't see
  can never be a query target either. Confirmed via `rpc/deployment_test.go`, a dedicated
  testcontainer connecting as a genuine non-superuser `LOGIN` role.
  **Config namespace consistency pass** : `query.*`/`dmut.*` as two unrelated top-level
  namespaces was itself part of what made the `pg.query.user`-needs-broad-SELECT-just-
  for-introspection problem above surprising — unified under one `pg.*` namespace, every
  previously-inconsistent flat key given a `_`-separated word boundary
  (`pg.query.anonymous_role`, `pg.query.wellknown_path`, `pg.query.max_depth`,
  `jwt.cookie_name`/`same_site`/`max_age`/`renew_after`/`max_session_age`,
  `http.request_domain_name`/`response_domain_name`/`cookies_max_age`), and
  `http.functions.auth` renamed `http.functions.allowed_auth` to match
  `allowed_routes`'s own naming (both are restriction regexps ; `check_session`
  correctly keeps no `allowed_` prefix, since it names a function rather than
  restricting one). An intermediate version of this pass introduced a `pg.admin.*` /
  `pg.query.*` split mirroring `Pg`/`PgQuery` symmetrically — reverted before landing :
  `set role` per request, not the connecting login's own privileges, is what actually
  restricts a request's data access, so requiring two separately-configured logins just
  to start rel was friction without a real safety payoff. Settled shape : `pg.uri` (a
  full `postgres://user:pass@host:port/db` string, authoritative when set — the
  granular fields below are ignored, not merged) or `pg.user`/`pg.password`/`pg.host`/
  `pg.port`/`pg.database` as the ONE required primary connection, used for dmut
  migrations, startup introspection, AND request-serving by default ;
  `pg.query.user`/`pg.query.password` remain as an OPTIONAL, documented-and-encouraged-
  but-never-required narrower login for request-serving specifically. `pg.
  NewInfosAdminQuery` (`pg/info.go`) still introspects via the primary connection and
  builds the request-serving pool from the query login separately whenever that IS
  configured — two distinct connections, not one shared pool, so setting `pg.query.*`
  stays meaningful — but degenerates to one shared pool (`NewInfos`) when it isn't,
  matching the simplest possible `--pg.uri` setup. The second (introspection) bug above
  is resolved by this shape either way : introspection never needs a grant on the
  narrower login purely to succeed, since it never runs as that login. `http.static.path`
  (default `/static`) also added as a placeholder — see "Named but empty" below — nested
  under `http.static.*` since more related keys (path-based access control) are
  anticipated once that feature is actually specced.
- **`dmut` / migrations** (`03-dmut.md`) — RESOLVED, implemented. `03-dmut.md` now fully
  specifies `dmut.path`/`dmut.reload_drain_timeout`, boot ordering (dmut runs before
  introspection, always, log-and-continue on failure), and the `SIGUSR1` reload mechanics
  (maintenance mode + bounded drain + context cancellation of stragglers + dmut rerun +
  reintrospection + registry rebuild + atomic handler swap). Implemented as :
  `github.com/ceymard/dmut/v2`'s `mutations` package driven as a library (never the CLI
  binary) via the new `dmut` package (`dmut/run.go`) ; the reload-aware `http.Server.Handler`
  wrapper and 7-step reload orchestration live in the new `boot` package
  (`boot/handler.go`/`boot/reload.go`) ; `pg.ReIntrospect` (`pg/info.go`) shares its
  introspection body with `pg.NewInfosAdminQuery` and reuses the existing request-serving
  pool rather than opening a new one. `cmd/rel/main.go` wires dmut before introspection at
  startup and a persistent `SIGUSR1` handler alongside the existing shutdown signal
  handling. This also resolves the `01-introspection.md`/`SIGUSR1` reload question this
  same TODO bullet above ("Introspection lifecycle") used to reference.

## Named but empty

- **`oauth-openid-saml.md` — 0 bytes.** `jwt-roles-and-http.md` names the libraries
  (`crewjam/saml`, `go-oidc`) and how a session gets minted once authenticated, but the
  actual `/auth/*` routes are unspecified : the SAML ACS endpoint and the OIDC callback
  specifically, since those protocols mandate fixed, redirect-driven callback URLs that
  can't be modeled as an ordinary `/rpc/{schema}/{function}` call. **Re-examined and
  narrowed** : username/password login is NOT actually missing a contract — it needs no
  special route or Postgres function signature at all, since it's just an ordinary route
  function (`auth.login(req: RelHttpRequest) returns RelHttpResponse`) using the
  already-fully-specified `RelHttpResponse.jwt` mint mechanism (checks credentials
  however the developer wants — bcrypt, an extension, whatever — sets `jwt` on success,
  gated by `http.functions.allowed_auth` like any other route function). The genuinely open gap
  is narrower than previously stated : only SAML/OIDC's own protocol-mandated redirect/
  callback endpoints, not login in general.
- **`/api/{schema}/{function}` renamed to `/rpc/{schema}/{function}`** throughout
  `jwt-roles-and-http.md` — `/api` named nothing about the actual mechanism ; `/rpc`
  matches PostgREST's own convention for the identical concept (named backend function
  callable over HTTP), which the spec already invokes as a comparison point elsewhere.
  **Now implemented** (`rpc/`) : discovery/registry against `RelHttpRequest`/
  `RelHttpResponse`/mimetype domains, dynamic `/rpc/{schema}/{function}` dispatch,
  `__VERB` suffix splitting, `allowed_routes`/`http.functions.allowed_auth` gating, and the full
  JWT lifecycle (`jwt/` : mint/sign/verify/renew, `check_session`, `SET LOCAL ROLE`
  inside the request's own transaction). Jet template rendering is also now implemented
  (`rpc/templates.go` — see "Static file serving" below, same commit). Still deferred :
  SAML/OIDC (see "Named but empty" below).
- **`jwt.anonrole` renamed to `query.anonymous_role`**, reconciling a genuine drift :
  `jwt-roles-and-http.md` and `querying.md` named what reads as the identical setting
  (the role applied to unauthenticated/unverifiable requests) under two different keys,
  neither doc cross-referencing the other. Both docs now use `query.anonymous_role`
  (default `~anonymous`, previously stated only under the wrong name).
  `config.Pg.Anonymous`/`config/loader.go` updated to match.
- **Renewal's `SameSite` behavior, previously unstated, resolved** : a renewed JWT cookie
  always uses `jwt.samesite`, never a `jwt_attrs.samesite` override from the original
  mint — unlike `jwt_attrs.maxage` (which self-persists, since renewal reuses the current
  token's own `exp - iat` width), `SameSite` isn't a JWT claim, so nothing survives
  renewal to reapply it, and a private claim just to carry one rarely-used cookie
  attribute wasn't judged worth it.
- **Static file serving** (`00-general.md`'s "configurable file access control based on path and
  database queries" bullet) — RESOLVED, spec'd AND IMPLEMENTED. `specs/04-http-content.md
  ## Static files` fully specifies, and the `static` package (`static/static.go`) implements,
  `http.static.path` (a colon-separated, PATH-style search list, same convention
  `pg.query.wellknown_path` already uses, first directory doubling as the one write target for
  `### Upload destinations`), the fixed `/static/` mount, no-listing/no-dotfiles defaults, and the
  database-query-driven access control this bullet named but never detailed (`### Access control` —
  named, prefix-scoped rules reusing `http.functions.check_session`'s function-name/RSxxx-reject
  interaction shape). `### Upload destinations` (`rpc/upload_registry.go`/`rpc/upload_handler.go`)
  implements the two-function shape family, both mandatory, sharing one JSON domain (`RelUpload`,
  `http.upload_domain_name`) reused progressively : `<name>__prepare(req, part jsonb) returns
  RelUpload` (read-only-transaction-enforced, runs before any bytes are received, makes a BINDING
  placement decision — path/mkdir/overwrite) and `<name>(req, upload RelUpload) returns
  RelHttpResponse` (the only one that writes to the database, runs after bytes are already safely
  on disk, with `part`/`size` filled in by rel) — for a function to decide WHERE an upload lands
  without the bytes ever passing through Postgres — genuinely closer to real streaming than `files
  bytea[]` can offer, at the cost of being scoped to one file per request (no sibling form fields).
  The actual disk swap (rename-old-aside/rename-new-into-place) only happens AFTER the mandatory
  function's transaction commits, narrowing the DB-commit-vs-disk-write consistency gap to that one
  final same-filesystem rename — the same limitation class as `jwt-roles-and-http.md`'s own "no
  true streaming to Postgres" note, just a much smaller window than a naive design would leave.
  Same document's `## Templates` section is also now implemented (`rpc/templates.go`, Jet v6,
  `Data`/`Req`/`Nonce` template variables, buffered execution so a runtime error is a clean 500
  rather than a partial body), and `## CORS`/`## CSP` (the `websec` package) implement the
  security-header half of the same document, composed into one middleware applied uniformly to
  `/rel`, `/rpc`, and `/static`.
- **TypeScript/JS export** (`/js/query.js`, `/js/schemas/*.ts`). Named as a feature in
  `00-general.md`. No spec on how types are generated from introspection + well-known
  queries, or what the runtime query-building helper actually does.
- **Well-known queries.** Named in `querying.md ## Configuration` (`$param`, prepared
  statements, exported to the TS client) but never given its own section : where they're
  defined/stored, `$param` casting rules, caching and versioning across schema reloads.

## Open questions already flagged, still unresolved

- **RESOLVED, remove from future passes** : `querying.md ### Join eligibility`'s FK-pairing
  question is no longer live — the `> Question:` marker itself is gone from the spec, and its
  text (a real FK's declared pairing rejects a mismatched `on`) matches
  `pg/info_constraint.go:319-333`'s `ResolveJoin` exactly. Left here as a record of when this
  was confirmed, not as an open item — the next pass over this file should delete this bullet
  entirely rather than re-check it.
- `querying.md ## Errors` — the `RelErrorResponse` ENVELOPE shape itself is now settled and
  accurate (`{status: "error", error, pg_error?}`, matching `server/response.go`'s
  `errorResponse` and tested in `server/rel_test.go` — the spec's own earlier draft shape,
  `status_code`/`message`/`stacktrace`/`sql_statement`/`data`, was never built and has been
  replaced in the spec text). What's still genuinely open : `error` is always the underlying Go
  error's own message text, not a stable, machine-readable code — no taxonomy exists.
- `02-error-handling.md ## Error Codes` was rewritten to describe only what's actually
  implemented (the `RSxxx` convention, `pgerr` package) — this surfaced a real, unresolved
  design question rather than just a stale-doc one : the previous text specified an
  `X-Rel-Errorcode` response header and an `errorcode` JSON property, neither of which exists
  anywhere in the code (`grep`-confirmed). Needs a decision : was this abandoned deliberately,
  or is it still-intended work that just hasn't landed yet ? If the latter, it belongs back in
  this document as an explicit "not yet implemented" item, not silently dropped.
- `00-general.md` vs. `05-typescript.md` disagree on where the generated TS/JS client files
  live — `00-general.md` says `/js/query.js`/`/js/query.ts`/`/js/schemas/schema1.js`;
  `05-typescript.md` says `/rel/query.js`/`/rel/query.ts`/`/rel/db.json`/`/rel/db/<schema>.json`.
  Neither path is implemented in code, so nothing today favors one over the other — needs a
  decision, not a guess.
- `05-typescript.md` has two mid-sentence truncations (line 16, "...but also eventual
  libraries that would want to _" ; line 25, "...given schema.json," with nothing after) —
  needs the redactor's own original intent, can't be completed by inference.
- `query.ts`'s `arguments` (function-relation) writability note — "this *might* be a
  problem to leave it writable, but I can't think why." Never promoted to a tracked
  `> Question:`, still just sitting in a comment.
- `01-logging.md ## Domain scoping` — section header only, no content. Request-ID header
  name also marked TBD.

## Not started

- CLI / entrypoint structure — no section on process structure, subcommands (is `dmut` a
  subcommand or purely signal-driven?), startup sequence, graceful shutdown.
  **Partially resolved** : `cmd/rel/main.go` now implements the full `config` package
  (`01-configuration.md`, koanf-based file/env/flag merge, `$FILE$`, "no arrays",
  `ConfigReader`) and the `logging` package (`01-logging.md ## Configuration`/
  `## Logger construction` only — request-scoped middleware, access logging, and the
  `oops` `logging.Error` helper are still unimplemented, see those packages' own doc
  comments), then serves `POST /rel` with graceful SIGINT/SIGTERM shutdown. Two things
  this pass had to invent, not spec'd anywhere : `http.host`/`http.port` (the HTTP bind
  address — no section defines one) and `pg.database` (`config.Pg` had no field naming
  which database to connect to at all, needed to build a real connection URI). Also one
  deliberate reading of `01-configuration.md ## Error handling and secrets` worth
  recording : `ConfigReader`'s accessors do NOT log a plain "key absent" miss (only a
  genuinely malformed — present but wrong-type — value is logged), since nearly every
  call site uses an `*OrDefault` variant, for which "absent" is the expected, extremely
  common case ; logging every unset optional key at Error level on every startup would
  be pure noise unrelated to an actual problem. A malformed value is still fatal even
  through an `*OrDefault` call (see `config.ConfigReader`'s own doc comment for how).
  Still missing : `dmut` subcommand-vs-signal question, auth/`SET LOCAL ROLE` wiring.
- Testing conventions — `AGENTS.md` mandates testcontainers ; no documented fixture/schema
  convention. `pg/testdata/schema.sql` is one ad hoc instance, not a written-down pattern.
