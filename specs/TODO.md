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
  as SQL shape and recursion rules, not as Go types. `query/sql.go` is still a 26-line
  stub with empty method bodies ; nothing has a designed home yet for node-tree
  construction, walking `ResolveJoin` per join, or the function/relation blacklist checks
  from `querying.md ### Scoping`.
- **Connection pool / transaction lifecycle.** Never given its own spec, but three other
  documents assume it exists : `SET LOCAL ROLE` timing (`jwt-roles-and-http.md`), the
  commit-before-select response design (`querying.md ## Response Shape`), and `_data`
  being a session-scoped temp table that must be guaranteed clean across pooled
  connection reuse.
- **`dmut` / migrations** (`03-dmut.md`, 14 lines). Legacy's `dmut` is a DAG/content-hash
  migration tool, a separate vendored module (`github.com/ceymard/dmut`) — not a
  sequential up/down tool. The current spec doesn't say whether rel keeps using that
  library, what changes, the migration file format, transaction/idempotency guarantees,
  or what "wait for the last request to finish" actually does to requests that arrive
  during that wait.

## Named but empty

- **`oauth-openid-saml.md` — 0 bytes.** `jwt-roles-and-http.md` names the libraries
  (`crewjam/saml`, `go-oidc`) and how a session gets minted once authenticated, but the
  actual `/auth/*` routes are unspecified : the SAML ACS endpoint, the OIDC callback, the
  username/password login route, and — concretely missing — the Postgres function
  contract for username/password auth (unlike `check_session`, which has a fully
  documented signature, "delegated to a Postgres function" is as far as this goes).
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
- Testing conventions — `AGENTS.md` mandates testcontainers ; no documented fixture/schema
  convention. `pg/testdata/schema.sql` is one ad hoc instance, not a written-down pattern.
