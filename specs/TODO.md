# Spec completeness TODO

What's still missing before the specs in this directory are complete enough to start
implementing, measured against the feature list in `00-general.md` (the stated intent).
Grouped by how much they block implementation, not by file.

## Blocking / foundational — nothing downstream can be built confidently without these

- **Introspection lifecycle** (`01-introspection.md`, now written up). The mechanism
  itself exists and is tested (`pg/`), and its contract is now documented, but the spec
  still doesn't say how it's wired into the running server : what's exposed to the query
  compiler, and — the sharp edge — how the `SIGUSR1` reload swaps the schema cache without
  racing requests already in flight. Tracked as an explicit `> Question:` in
  `01-introspection.md` rather than resolved here.
- **Query compiler architecture.** `querying.md` specifies the Reading/Writing algorithms
  as SQL shape and recursion rules, not as Go types. Pass 1 (tree inflation + DB
  resolution) is now implemented and tested (`query/node_parse.go`, `query/node_resolve.go`,
  `pg/info_searchpath.go`, `pg/info_lookup.go`) : JSON decode, relation/function
  resolution (search path + blacklist), `ResolveJoin` per join, write_mode
  defaulting/validation, `on_conflict`/insert/update-column resolution, `query.maxdepth`
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
  open : `SET LOCAL ROLE` / auth timing (no auth exists yet at all, see the auth item
  below) ; whether a read-only `Sequence` (no write item at all) should also share one
  transaction across its queries — right now it doesn't, see the note added to
  `querying.md ## Transactions`.
- **`dmut` / migrations** (`03-dmut.md`, 14 lines). Legacy's `dmut` is a DAG/content-hash
  migration tool, a separate vendored module (`github.com/ceymard/dmut`) — not a
  sequential up/down tool. The current spec doesn't say whether rel keeps using that
  library, what changes, the migration file format, transaction/idempotency guarantees,
  or what "wait for the last request to finish" actually does to requests that arrive
  during that wait.

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
  gated by `http.functions.auth` like any other route function). The genuinely open gap
  is narrower than previously stated : only SAML/OIDC's own protocol-mandated redirect/
  callback endpoints, not login in general.
- **`/api/{schema}/{function}` renamed to `/rpc/{schema}/{function}`** throughout
  `jwt-roles-and-http.md` — `/api` named nothing about the actual mechanism ; `/rpc`
  matches PostgREST's own convention for the identical concept (named backend function
  callable over HTTP), which the spec already invokes as a comparison point elsewhere.
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
- **Static file serving.** One bullet in `00-general.md` ("configurable file access
  control based on path and database queries"). No detail anywhere.
- **TypeScript/JS export** (`/js/query.js`, `/js/schemas/*.ts`). Named as a feature in
  `00-general.md`. No spec on how types are generated from introspection + well-known
  queries, or what the runtime query-building helper actually does.
- **Well-known queries.** Named in `querying.md ## Configuration` (`$param`, prepared
  statements, exported to the TS client) but never given its own section : where they're
  defined/stored, `$param` casting rules, caching and versioning across schema reloads.

## Open questions already flagged, still unresolved

- `querying.md ### Join eligibility` — the one live `> Question:` : the non-FK
  eligibility path checks column sets independently on each side, with no requirement
  that they correspond to an existing FK's actual pairing when one exists.
- `querying.md ## Errors` — `RelErrorResponse.error` is literally typed as `// error
  code, to be documented`. No taxonomy exists yet.
- `querying.md ## Errors` — `RelErrorResponse.stacktrace` shape marked "unclear at this
  moment."
- `query.ts`'s `arguments` (function-relation) writability note — "this *might* be a
  problem to leave it writable, but I can't think why." Never promoted to a tracked
  `> Question:`, still just sitting in a comment.
- `01-logging.md ## Domain scoping` — section header only, no content. Request-ID header
  name also marked TBD.
- `02-error-handling.md` (6 lines) — doesn't cross-reference `querying.md`'s
  `RelErrorResponse` shape, doesn't specify the dev-mode error page's route/content, and
  there's no central Postgres-error → HTTP-status mapping (only partial coverage, via
  `jwt-roles-and-http.md`'s `RSxxx` convention for route functions specifically).

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
