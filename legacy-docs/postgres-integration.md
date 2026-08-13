# Legacy Go Server: Postgres Integration Reference

Scope: how `sales-way.com/server` (the legacy Go binary, package `main` plus
sub-packages `pg`, `query`, `rel`, `templates`, `sw`) talks to Postgres, and
what runtime/config surface exists today. Auth/JWT env vars are covered fully
in a separate doc; here they are only mentioned where they intersect with DB
role-switching.

All line refs are relative to `/home/chris/Code/rel/_legacy/`.

## 1. Purpose / division of labor

**Correction (verified against the actual `Dockerfile` at the user's
request):** this checkout does **not** run PostgREST alongside the Go
server. The `Dockerfile` (`Dockerfile:1-26`) builds a `scratch`-based image
containing nothing but the statically-linked Go binary — one `ENTRYPOINT
["/server/server"]`, no supervisor, no second process, no PostgREST binary
anywhere in the build. `root/` (`config.sh`, the s6-overlay service scripts,
`postgrest.conf`) describes an **older/retired deployment generation** that
did run the Go server and PostgREST side by side under s6 — it is not wired
into today's `Dockerfile` or `Makefile` `image`/`upload` targets at all (confirmed by grep: nothing in `Dockerfile` or `Makefile` references
`root/`). See [`infra-plugins-misc.md`](infra-plugins-misc.md) §6 for the
detailed internal evidence that `root/config.sh` doesn't even consistently
describe one base image (mixes Alpine's `install_packages`/`ash` with
Debian/Ubuntu's `apt`), independent of whether it ever matched the
`Dockerfile`.

In other words: by this point the Go server has already **absorbed**
PostgREST's job rather than merely competing with it from inside the same
container. The custom query/write engine (`/rel`, `pg2.go`, documented in
[`query-language.md`](query-language.md)) is not a "hand-rolled competitor
running next to PostgREST" — for this version, it **is** the REST-over-
Postgres API; there is no PostgREST process left to compete with in the
actual deployed artifact.

What the `PGRST_*`-prefixed env vars (`PGRST_DB_URI`, `PGRST_DB_SCHEMA`,
`PGRST_DB_ANON_ROLE`, `PGRST_JWT_SECRET`, `PGRST_JWT_COOKIE`) still read by
`env.go`/`main.go` mean, given the above: they are **inherited variable
names**, kept as fallback aliases purely so existing deployment configs
(env files, k8s secrets, etc.) written back when PostgREST *was* co-located
don't have to be renamed immediately — not evidence that a PostgREST
process is being configured or launched anywhere by this code. Nothing in
any `.go` file reads `root/etc/postgrest.conf`, execs `postgrest`, or proxies
to a PostgREST port; grepping the whole tree for `exec.Command`/`os/exec`
usage referencing `postgrest` returns nothing.

Responsibilities as implemented today (per `main.go:1-19` header comment and
actual code) — all in the one Go process:

- OAuth/SAML/password login, file uploads/sends (via nginx X-Accel-Redirect),
  static file / SPA serving, websockets.
- A custom query language for CRUD with relationship embedding (`/rel`,
  `pg2.go`) — the live, working data API, filling the role PostgREST used to
  fill.
- A newer, JSON-based parser for the same idea (`/rel2`, `pg3.go`, described
  in §7 — currently a stub/debug endpoint, not wired to Postgres execution).
- On-the-fly Postgres→TypeScript schema generation (`/scripts/{schema}.ts`),
  schema introspection caching, and driving `dmut`/`dmut2` migrations.

A reverse proxy in front (nginx, judging by the `X-Accel-Redirect` comment)
is still presumably expected for TLS termination/static routing, but there is
only one backend process for it to route *application* traffic to now.

One config asymmetry worth carrying into the rewrite: `root/etc/postgrest.conf`
(the retired config, §6 of the infra doc) exposed a configurable listen port
(`PGRST_SERVER_PORT`), whereas the Go server's port is **hardcoded to
`"3001"`** (`main.go:107`, `srv.Port = "3001"`) with no env var override at
all — worth fixing regardless of PostgREST's absence, since it's still a
missing piece of basic deployment flexibility.

## 2. Environment variables (DB connection / schema)

Read via `getenvOrDefault(vars...)` (`env.go:12-24`), which returns the first
non-empty env var among the arguments, with the **last** argument acting as
a literal default (not looked up as an env var name). It also mentions (in
its docstring) checking `/var/run/secrets/<var>.txt` for container secrets,
but the implementation shown does **not** actually read that path — it's a
stale comment (see §9).

| Variable | Where read | Meaning / default |
|---|---|---|
| `DATABASE_URL`, `PGRST_DB_URI` | `main.go:81` | Postgres connection string for the Go server's own pool. Checked in that order; if both empty, `db_url == ""` and the server runs with `srv.Pool == nil` (no DB). |
| `PGRST_DB_SCHEMA` | `main.go:84-85` | Comma-split into `srv.Schemas []string` via a bare `strings.Split(schems_def, ",")` — **no whitespace trimming** (default `"public"`). This is the set of schemas the Go server will introspect/expose (see §3) — same variable name PostgREST itself uses for its own exposed schema(s), so the two processes stay in sync by construction, *as long as the value has no spaces around commas*. A value like `"public, other"` produces schema names `"public"` and `" other"` (leading space); every `slices.Contains(schemas, ...)` check downstream (`pg/types.go:389,396,469`, `pg/relationships.go:42`) then silently fails to match the real `other` schema, so its tables load into memory but are never marked exported. Contrast `VIRTUAL_HOST` right below it in the same function, which *does* get whitespace-tolerant splitting via a POSIX regex (`main.go:105-106`) — an inconsistency worth not repeating. |
| `VIRTUAL_HOST` | `main.go:104-113` | Not DB-related but drives `srv.Hostnames`; warns if left at `"localhost"`. |
| `SW_ENABLE_TS_SCHEMAS` | `env.go:45` | Gates the `/scripts/{schema}.ts` TypeScript-generation route (§5). |
| `SW_DB_ANON_ROLE`, `PGRST_DB_ANON_ROLE` | `env.go:39` | `DB_ANON_ROLE` — anonymous Postgres role, used to decide 401 vs 403 on permission errors (`pg2.go:114-119`) and as a JWT-role fallback (`jwt.go:155-164`). Same variable PostgREST uses for `db-anon-role`. |
| `PGRST_DB_ANON_ROLE` (again, different default) | `websocket-session.go:65` | A *second*, independent read of the same env var, defaulting to `"@unauthenticated"` instead of `""` — inconsistent with `DB_ANON_ROLE`'s default (see §9). |
| `SW_IMPERSONATOR_ROLE` | `env.go:40` | `DB_IMPERSONATOR_ROLE`, declared but not observed to be used in the files reviewed here (dead/unused in this slice). |
| `SW_ENABLE_DEBUG` | `env.go:44` | `DEBUG_ENABLED`; when `"true"`, adds an `X-Db-Role` response header (`pg2.go:67-69`) and enables SQL stack traces on error (`stacktrace.go:34`). |

Variables that only ever meant anything to PostgREST's own config file
(`root/etc/postgrest.conf:1-16`, the retired deployment mode — see §1):
`PGRST_DB_POOL`, `PGRST_SERVER_HOST`, `PGRST_SERVER_PORT`, `PGRST_JWT_SECRET`
(Go's own JWT handling reads `SW_JWT_SECRET` / `PGRST_JWT_SECRET` too, see
`env.go:36`, but that's an auth concern — the alias exists independently of
PostgREST), `PGRST_SECRET_IS_BASE64`, `PGRST_ROLE_CLAIM_KEY`,
`PGRST_MAX_ROWS`, `PGRST_PRE_REQUEST`. None of these are read anywhere in
the Go code in this checkout — they would only matter if `postgrest.conf`
and the `postgrest` binary were reinstated into the build. Setting them
today has **zero effect** on anything the Go server does; the Go server's
own `pgxpool.Pool` only ever consumes `PGRST_DB_URI` and `PGRST_DB_SCHEMA`
(read as fallback aliases of `DATABASE_URL`/native `SW_`/`PGRST_` names, for
config-compatibility reasons, not because a PostgREST process consumes them
too).

### Connection pool construction and retry/backoff

`main.go:91-100`:
```go
pool, err = pgxpool.New(context.Background(), db_url)
for err != nil {
    log.Printf("[!] Failed to connect to database : %s, retrying in 1 second", err)
    time.Sleep(1 * time.Second)
    pool, err = pgxpool.New(context.Background(), db_url)
}
```
- Only attempted if `db_url != ""`.
- **No pool-size or timeout options are passed** — `pgxpool.New` parses
  `db_url` itself, so any pool tuning (`pool_max_conns`, etc.) must come from
  query-string parameters on the connection string itself; there is no Go-side
  equivalent of PostgREST's `db-pool` setting.
- Note: `pgxpool.New` typically does **not** itself dial the DB (it only
  parses config and lazily connects), so this loop mostly guards against
  malformed connection strings, not an actually-down DB. The real
  reachability check is the second loop below.
- Then, only if a pool was created (`main.go:116-137`), the server tries up to
  5 times to `Pool.Acquire` a connection, sleeping 5s between attempts; if all
  5 fail, it logs an error and **returns from `main()` entirely** (the process
  exits without serving anything — no supervisor-level restart triggered
  intentionally, though s6/`entr` may respawn it).

## 3. Schema introspection — `PgReloadTables`

`srv.PgReloadTables()` (`sw/defs.go:108-113`) calls `pg.ReloadTables(srv.Pool,
srv.Schemas)` (`pg/types.go:210-496`) and rebuilds `srv.RootScope` for the
query language (`query.NewRootScope`). It is triggered:
- Once at startup, **after** auth/OAuth/SAML setup, near the end of `main()`
  (`main.go:200-202`).
- On `SIGUSR1` (`main.go:297-316`), after re-running `dmut`/`dmut2`
  migrations — i.e. "reload schema after migration" is the intended
  operational flow, plus `ReloadLoginFunctions(&srv)` right after.
- It is *not* file-watched or polled; it is purely migration/signal driven.

What it queries, in one call using `pool.Query` on `context.Background()`
(not request-scoped):

1. **Tables/composite types** (`pg/types.go:218-346`) via one UNIONed SQL
   query combining:
   - `information_schema.columns` joined to a `pks` CTE
     (`information_schema.table_constraints` +
     `constraint_column_usage`) to mark PK columns, producing one JSON row
     per table (`json_agg`/`json_build_object`).
   - A `_constraints`/`_table_constraints` CTE built from
     `information_schema.table_constraints` +
     `key_column_usage` + `constraint_column_usage` to capture **all**
     constraints per table (PK, UNIQUE, FOREIGN KEY) including target
     schema/table/columns for FKs.
   - A `composite_types` CTE over `pg_type`/`pg_class`/`pg_attribute` (real
     `pg_catalog`, not `information_schema`) to also model **composite
     types** as pseudo-tables (`is_real_table: false`), so function
     argument/return composite types get column metadata too.
   - Column defaults: identity columns get a synthesized
     `nextval(pg_get_serial_sequence(...))` expression
     (`pg/types.go:298`); everything else uses `column_default` verbatim.
2. **Functions** (`pg/types.go:419-449`) via `pg_proc`/`pg_namespace`/
   `pg_language`/`pg_type` (pure `pg_catalog`), capturing name, language,
   strictness, security-definer flag, volatility, return type/schema,
   whether it returns a set, and an `argument` array (via
   `generate_series` over `pronargs`) with per-arg name/type/schema/mode.
   - Single-argument, non-set functions whose argument type matches a known
     table (`arg.Schema+"."+arg.Type`) are folded into that table as a
     **computed column** (`pg/types.go:471-488`) — this is how Postgres
     "computed column" functions (`create function foo(t mytable) returns
     ...`) become virtual columns in the exposed schema.

Caching / marking: each row is JSON-unmarshalled into `DBTable` (or
`Function`), keyed by `schema.name` in `DBAllTables`/`DBFunctionMap` maps
held on `srv.Tables` / `srv.Functions` (`sw/defs.go:24-25`) — an in-memory
cache, rebuilt wholesale on each `ReloadTables` call (no incremental
diffing). `IsExported` flags are set per table/column/function based on
whether their schema is in `srv.Schemas` (`PGRST_DB_SCHEMA`), i.e. schemas
outside that list are loaded into memory but marked non-exported and mostly
excluded from TS generation / relationship building (see §4 — cross-schema
FKs to a non-exposed schema are skipped entirely).

The whole thing runs under one `pool.Acquire` (`pg/types.go:212-216`),
released via `defer`. Errors from either query abort the whole reload:
`pg.ReloadTables` returns `nil, nil, err` (`pg/types.go:214,348,451`), and
`PgReloadTables` (`sw/defs.go:108-113`) assigns *all three* return values
unconditionally — `srv.Tables, srv.Functions, err = pg.ReloadTables(...)` —
so a failed reload **wipes the in-memory schema cache to `nil`** rather than
leaving the previous good schema in place. Worse, the very next line,
`srv.RootScope = query.NewRootScope(srv.Tables, srv.Functions, srv.Schemas)`,
then runs unconditionally too, building the query root scope from `nil`
tables/functions. See §9 — this is flagged as a risk for the rewrite
(reload should only swap the cache on success).

## 4. Relationships — `pg/relationships.go`

`DBAllTables.BuildRelationships(schemas)` (`pg/relationships.go:29-105`) runs
once per `ReloadTables` call, right after tables are parsed and PK/unique
groups are computed (`pg/types.go:417`). For every FK constraint on every
table:

- Skips the FK if either the local or the distant table's schema is not in
  the exposed `schemas` list (`pg/relationships.go:41-44`) — cross-schema FKs
  into unexposed schemas produce no relationship at all.
- Builds a forward `RelationShip` (local → distant, `IsReverse: false`) and a
  synthetic reverse one (distant → local, `IsReverse: true`), the latter
  marked `IsMultiple` unless the FK's local columns are already a unique
  group on that table (i.e. 1:1 vs 1:many is inferred from
  `UniqueColumnGroups`).
- Each relationship gets **multiple aliases** registered into
  `table.RelationshipsMap` (`addRelationship`, `pg/relationships.go:107-137`):
  a fully-qualified normalized key, `>>schema.table` / `<<schema.table`,
  `>>table` / `<<table`, `>>schema.table(cols)`, `>>constraint_name`, plus
  `[]`-suffixed variants for multi-row reverse relations. Colliding names
  across different relationships are intentionally set to `nil` in the map
  (`pg/relationships.go:157-163`) so an ambiguous name resolves to "not
  found" instead of silently picking one — callers must disambiguate.
- `PreferredForm()` (`pg/relationships.go:139-153`) picks the canonical
  `>>`/`<<` + table + `(cols)` [+`[]`] string used both as the map's
  preferred key and as the property name emitted in generated TypeScript
  (§5) and in the `relations: () => ({...})` blocks of `json_table`/typescript
  templates.

Downstream use: this relationship graph is what lets the `query`/`rel`
packages do relationship-embedding joins and nested-JSON DML (flattening
nested insert/update payloads into per-table temp tables) — covered in depth
in the query/rel design doc; here it's enough to know `srv.Tables[...].Fks`,
`.Incoming`, `.Relationships`, `.RelationshipsMap` are the shared data
structure both the `/rel` query engine and the TypeScript generator read
from.

## 5. TypeScript generation

Two source files produce (near-)identical output through different code
paths — both are dev-tooling to hand the frontend typed models of the DB
schema, not part of the runtime data API:

- **`pg_typescript.go`** (`setupPgTypescript`, `pg_typescript.go:160-200`) —
  registers `GET /scripts/{schema}.ts` **directly on `srv.Router`**, gated
  entirely by the deployment-time env var `SW_ENABLE_TS_SCHEMAS`
  (`ENABLE_TS_SCHEMAS != ""`, `env.go:45`, `pg_typescript.go:162`) — there is
  **no per-request authentication** on this route (it is not registered on
  the jwt-wrapped sub-router, see §7); anyone who can reach the port gets
  the full introspected schema (table/column names, types, constraints,
  relationship graph) for the requested schema once this feature is turned
  on. Special-cased path `model` returns a static,
  embedded file `web/static/scripts/model.ts` (`pg_typescript.go:24-25`)
  verbatim — presumably the hand-written runtime support library
  (`Model`/`ModelWithPk`/`Rel`/`call`) that generated code imports from
  `"./model"`. For any other `{schema}`, it filters `srv.Tables`/
  `srv.Functions` down to that schema, sorts them by name, and calls
  `pg.OutputTypescriptSchema(...)`.
- **`pg/typescript-output.go`** (generated from **`pg/typescript-output.pat`**
  — see below) implements `OutputTypescriptSchema`, which is the function
  actually invoked. It writes: an `import * as s from "@salesway/scotty"` +
  `import { Rel } from "./model"` header, `import * as <schema>` lines for
  every foreign schema referenced by a relationship, `Multiple`/`SingleRow`
  const aliases, then per table an `export const <Table> = { name, fields:
  {...}, computed_fields?: {...}, relations: () => ({...}), pk: [...] }`
  object — i.e. a **runtime schema descriptor object**, not a `class`.
- **`pg/typescript.go`** is *not* wholly dead code — it's two things bundled
  in one file:
  - Shared, load-bearing helpers used by the *live* generator above:
    `TypescriptWriter` (the indentation-aware writer type itself, used as
    `ø *TypescriptWriter` throughout `pg/typescript-output.go`),
    `SerializerFor` / `BaseSerializerFor` (`typescript.go:66-111`, called
    repeatedly as `ø.SerializerFor(...)` from `OutputTypescriptSchema`), and
    `TypescriptName` / `baseTypeTypescriptName` (`typescript.go:113-152`).
    These map Postgres schema/type names to `@salesway/scotty` serializer
    expressions and TS type strings and must be kept if porting the live
    generator forward.
  - An orphaned, unused-by-any-route family: `WriteTypescriptFile` /
    `WriteTypescriptModelClass` / `WriteTableSerializer` /
    `WriteTypescriptRelations` / `WriteFunctionBody`
    (`typescript.go:154-389`), confirmed via `grep` to have no callers
    anywhere in the tree outside their own mutual recursion. This emits an
    **ES `class ... extends Model` / `ModelWithPk`** declaration style with
    `@(serializer)` decorator-style annotations and `@salesway/pgts` /
    `p.PgType<...>` fallbacks for unknown Postgres types — a different,
    earlier/alternative generation design from the plain-object
    `export const X = {...}` style that `pg_typescript.go`'s route actually
    calls. See §9.
- **`pg/types.go`** supplies the data model (`DBTable`, `DBColumn`,
  `Function`, `RelationShip`, etc.) both generators walk; it has no
  TypeScript-specific logic itself besides `SnakeCaseClassName`.

Output location: nothing is written to disk by this route — it's rendered
directly into the HTTP response (`w` is the `http.ResponseWriter`), i.e. the
frontend build/dev-tooling is expected to `curl`/fetch
`/scripts/{schema}.ts` (and `/scripts/model.ts`) to materialize `.ts` files
at build time.

### `.pat` files

`pg/typescript-output.pat` (and the two `templates/*.pat` files, §6) are
source files for a **custom Go text-templating/code-generation tool** (not
part of stdlib `text/template` — the syntax uses a bespoke sigil scheme):

- `@{ ... }` — a raw Go code block (declarations, loops with side effects,
  helper funcs) copied through more or less verbatim.
- `@func Name(args) { ... }` — declares a Go function whose *body* is the
  template; every line/token outside `@{...}` is treated as literal text to
  be `Write`n, with `@expr` substituting a single Go expression's string
  value inline (e.g. `@table.Name`, `@sel.DbTableName()`).
- `@if / @elseif / @else / @for / @continue` — control-flow keywords mirrored
  1:1 into the generated Go `if`/`for`, with the template body inside braces
  becoming the corresponding Go block's literal-text-emission statements.
  E.g. `@if rel.DistantRelation.Schema != table.Schema { @(rel.DistantRelation.Schema). }`.
  compiles to a Go `if` wrapping a `w.Write([]byte(rel.DistantRelation.Schema))`.
  `@"}"` is the escape for a literal `}` character in the output text (since
  `}` is the template's own block terminator).
  `@...expr|sep` (seen in `json_flat.pat:36-37,41,60,67`) is a "join" helper —
  iterate `expr` and interleave literal `sep` — e.g.
  `@...`"`rel...DistantColumnsNames`"`|`, `` emits a comma-joined,
  double-quoted column list.
  `@for|", " _, col := range ... { ... }` is a for-loop variant that
  auto-inserts `sep` between iterations without an explicit `if not first`
  guard, contrasting with the hand-unrolled `øsepN` boolean flags visible in
  the *compiled* `.go` output (`templates/json_flat.go:106-127` etc. —
  compare `.pat` line 61 `@for|", " ...` to the generated `øsep12` dance in
  `json_flat.go:117-127`).
- The compiled `.go` counterpart for each `.pat` is checked in alongside it
  (`pg/typescript-output.go` next to `.pat`; `templates/json_flat.go` /
  `json_table.go` next to their `.pat`s) — i.e. **generation is a build-time
  step, not a runtime one**; the `.go` files are committed, presumably
  regenerated by some external tool (name/binary not present in the files
  reviewed) whenever the `.pat` changes. Each generated file ends with a
  throwaway `TestIncludeN` function whose only job is to keep `io`/`fmt`/
  `bytes`/`testing` imports from being flagged unused — a generation-tool
  artifact, not a real test.

## 6. `templates/json_flat.*` and `templates/json_table.*`

Both are `.pat`-driven SQL generators (package `templates`) consumed
exclusively by `pg2.go`'s `/rel` handler (`SetupPG2Routes`,
`pg2.go:255-268`) to turn a resolved `query.AstSelection` AST into literal
SQL text written into a `bytebufferpool` buffer, which then becomes one
statement in the ordered `[]SqlRequest` batch executed in a single
transaction (`runJsonArraySql`, `pg2.go:34-135`).

- **`json_table.pat` / `json_table.go`** — `GenerateDeleteQuery` (only
  function in the file): wraps a `DELETE FROM <table> WHERE <where> RETURNING
  *` in a `CREATE TEMP TABLE ... ON COMMIT DROP AS WITH <name> AS (...)
  SELECT * FROM <name>` shell, so the delete's result set becomes a
  queryable temp table like everything else in the batch. Used for
  `query.OP_DELETE` (`pg2.go:266-267`).
- **`json_flat.pat` / `json_flat.go`** — the meat of nested-JSON
  insert/upsert/merge handling, three mutually-recursive functions:
  - `OutputDmlTable2` — for a given selection node, builds a CTE
    (`<sel>__insert`) that reads rows out of the shared temp table
    `__flat_json_temp` (a table the request populates via `COPY` with
    columns `___node_id, ___parent_id, ___selection_index, json`, see
    `pg2.go:74` and `pg2.go:280`) filtered to `___selection_index =
    <this node's index>`, `jsonb_populate_record`'d against the target
    table's row type, and resolves each column's value from: (a) a parent
    relation's already-inserted row (for outgoing FK columns owned by the
    parent), (b) a child relation's already-inserted row (for incoming FK
    columns owned by a child, i.e. children get inserted before parents in
    reverse-FK cases), (c) a `CASE WHEN ... IS NULL THEN <default>` fallback
    for `NOT NULL` columns with a default, or (d) the raw flattened `obj`
    value. Recurses into outgoing relationships first, then (at the bottom)
    into incoming ones.
  - `OutputDeleteStatement` — recursively emits `DELETE ... RETURNING *` CTEs
    per table in the tree. Its own delete CTE at the *current* node is only
    emitted when the `do_delete` parameter passed to that specific call is
    true, but the recursive calls **do not forward that parameter
    symmetrically**: recursion into `ResolvedOutgoingRelationships` always
    passes a hardcoded `false` (`json_flat.pat:12-17` /
    `json_flat.go:19-25`), while recursion into
    `ResolvedIncomingRelationships` always passes a hardcoded `true`
    (`json_flat.pat:20-28` / `json_flat.go:29-35`), **regardless of the
    caller's own `do_delete` value**. Net effect: every child/incoming
    relationship one level down always gets its own delete CTE emitted —
    `DELETE FROM <child> WHERE (pk) NOT IN (SELECT ... FROM <child>__insert)
    AND (fk cols) IN (SELECT ... FROM <parent>__insert)` — even when the
    *outer* operation is a plain `OP_INSERT` (`GenerateFlatQuery(..., false,
    false)`, `pg2.go:262`). See §9: this means nested children omitted from
    an insert payload can be deleted even though the verb is "INSERT", not
    "MERGE". The "not in the new incoming set" condition itself is
    expressed via `NOT IN (SELECT ... FROM <table>__insert)`.
  - `OutputInsertStatement` — emits the final
    `INSERT INTO <table> (...) SELECT ... FROM <table>__insert [ON CONFLICT
    (pk) DO UPDATE SET ...] RETURNING *` CTE, recursing into outgoing
    relationships *before* itself (parents must exist before children
    reference them) and incoming ones *after*.
  - `GenerateFlatQuery` — the entry point (`pg2.go:258,262,265`), wires the
    three together into one `CREATE TEMP TABLE <tmp> ON COMMIT DROP AS WITH
    __empty AS (SELECT true as empty) <dml ctes...> SELECT * FROM
    <table>__insert_for_real` statement.

  Called with `(do_delete, do_upsert)` = `(true,true)` for `MERGE`,
  `(false,false)` for plain `INSERT`, `(false,true)` for `UPSERT`
  (`pg2.go:256-265`) — i.e. this one template family implements all three
  non-delete write verbs by parameterizing which CTEs get emitted.

Net effect: the Go server converts a JSON tree with a "flattened" shape
(each node tagged with synthetic `___node_id`/`___parent_id`/
`___selection_index`) into a single multi-CTE SQL statement that
inserts/updates/deletes across the whole related-table tree atomically, then
selects the shape back out (a separate `SqlSelect` step, `pg2.go:290-303`,
not covered by these templates).

## 7. `pg3.go` / `setupPg3Routes`

Registers exactly one route: `POST /rel2` (`pg3.go:16`), **directly on
`srv.Router`**, not on the JWT+gzip-wrapped sub-router (`routes :=
srv.Router.With(jwt, gziphandler.GzipHandler)`, `pg.go:10-12`). Only `/rel`,
`/rel/*`, and `/_schema` are actually registered on that `routes` sub-router
(`SetupPG2Routes` receives it as its `router` parameter and calls
`router.Handle`/`router.Get` on it, `pg2.go:195,321-333`). `setupPgTypescript`
(§5, `/scripts/{schema}.ts`) is called separately in `pg.go:16` but
registers on `srv.Router` directly too (`pg_typescript.go:163`) — so it is
in the same boat as `/rel2`: **neither `/rel2` nor `/scripts/{schema}.ts`
passes through `jwtMiddleware`**, i.e. no valid JWT cookie/role claim is
required to reach either. `/rel2` has no other gate at all (open to anyone
who can reach the port); `/scripts/{schema}.ts` is gated only by the
deployment-time env var `SW_ENABLE_TS_SCHEMAS` being non-empty (§5), not by
anything per-request.

Behavior: reads the raw request body, parses it with
`rel.ParseFromBytes(srv.Tables, srv.Functions, body)` (a newer/alternate
parser in the `rel` package — distinct from, but layered on top of, the same
underlying `query` package types used by `pg2.go`/`templates`), resolves it
against a child of `srv.RootScope`, and for each resulting operation returns
a small JSON summary: `{relation, readonly, data_count?, flat_rows?}`. It
also calls `op.FlattenJsonData(body)` (same flattening step as `/rel`'s
write path) purely to report `len(flats)`, and dumps the parsed ops to
stdout via `pp.Println(ops)` (`pg3.go:69`) — a debug print left in.

**It never touches `srv.Pool` or executes any SQL.** There is no call to
`templates.GenerateFlatQuery`, no transaction, no `SET LOCAL ROLE`. This
reads as a **parser/resolver smoke-test endpoint** for the `rel` package —
useful for verifying that a payload parses and flattens correctly, not a
working data API today.

## 8. Connection pool lifecycle and error handling

- Pool creation and the initial `Acquire` retry loop are described in §2.
- Per-request usage: every handler that talks to Postgres (`pg2.go:36`,
  `sw/defs.go:64-106`) calls `srv.Pool.Acquire(ctx)` fresh and `defer
  conn.Release()` — no long-lived connections are held across requests;
  `pgxpool.Pool` itself manages the actual TCP connections and any
  reconnection after Postgres restarts (not custom logic in this codebase).
- `sw.SwServer` exposes three thin helpers (`sw/defs.go:64-106`) —
  `PgSimpleExec`, `PgSimpleQuery`, `PgSimpleQueryRow` — each of which
  explicitly returns an error (`"no pg pool"`) if `srv.Pool == nil`, so
  callers elsewhere in the codebase can run without a DB configured as long
  as they check `srv.HasPg()` first or tolerate the error.
- `/rel`'s handler (`runJsonArraySql`, `pg2.go:34-135`) opens one transaction
  per request, runs a `SET LOCAL ROLE "<jwt role>"; SET LOCAL
  "app.current_role" = '<role>';` (`pg2.go:62`) before anything else — this
  mirrors PostgREST's own role-switch-via-GUC approach, meaning both
  processes rely on Postgres-side RLS/grants keyed off the same role names —
  then executes each queued `SqlRequest` in order, committing at the end (or
  rolling back if any step errored). A Postgres permission error (SQLSTATE
  `42501`) is translated to HTTP 401 if the role equals `DB_ANON_ROLE`, else
  403 (`pg2.go:112-123`); any other query error becomes a 500 with the SQL
  text inlined into the response body (a debugging convenience but also an
  information-disclosure risk in production, see §9).
- If Postgres becomes unreachable **after** startup (mid-run outage): there
  is no explicit health-check/reconnect logic — the next request's
  `Pool.Acquire`/`Exec`/`Query` will simply error, `runJsonArraySql` returns a
  500, and the `/heartbeat` route (`main.go:322-341`) will keep reporting
  `db: "ok"` as long as `srv.Pool != nil`, i.e. **heartbeat only checks that a
  pool object exists, not that it can currently reach Postgres** — a false
  "ok" during an outage.
- `SIGUSR1` triggers `tryRunDmut` + `PgReloadTables` + `ReloadLoginFunctions`
  (`main.go:297-316`) — if `tryRunDmut` errors, the schema reload is **skipped
  entirely** (only happens in the `else` branch, `main.go:309-312`), so a
  failed migration leaves the old cached schema in place, but a successful
  migration always forces a fresh introspection.

## 9. Notes / things to reconsider for the rewrite

- **`pg.go` / `pg2.go` / `pg3.go` naming strongly suggests three successive
  iterations, and the call graph confirms partial supersession, not clean
  replacement:**
  - `pg.go` (`SetupPG`, 19 lines) is the current entry point wiring
    everything together — it is *not* itself a rewrite target, just glue.
  - `pg2.go` is the actual working, JWT-authenticated, transactional
    read/write data API (`/rel`, `/_schema`) — this is "v1" of the real
    engine despite the "2" in the filename (there's no `pg1.go`; the
    numbering seems to track the query-language generation, i.e. `query`
    package = "v2 language", `rel` package = "v3 language").
  - `pg3.go` (`/rel2`) is a **parser/resolver-only stub** for a newer
    `rel`-package-based language — it never executes SQL. It looks like
    active work-in-progress rather than dead code, but as shipped it is not
    a functioning replacement for `/rel`.
  - **Bug: `setupPg3Routes` is called twice** when `srv.HasPg()` — once
    inside `SetupPG` (`pg.go:15`) and again directly in `main.go:186-188`.
    Registering `POST /rel2` twice on the chi router should be flagged;
    depending on chi version this either silently keeps the last
    registration or panics at startup. Either way it's clearly unintentional
    and worth cleaning up in the rewrite (also drop the duplicate route from
    whichever call site is meant to be canonical).
  - **`/rel2` bypasses the JWT/gzip middleware stack** that `/rel`, `/rel/*`,
    and `/_schema` use (§7) — it requires no authentication whatsoever. Since
    it also never touches `srv.Pool`, there's no Postgres-level gate either
    (no `SET LOCAL ROLE`, no grants/RLS in play) — today it is simply open.
    If this endpoint is meant to become the eventual replacement for `/rel`,
    the rewrite must not forget to wrap it in the same auth chain, and
    should decide whether it should also gain the `SET LOCAL ROLE` step and
    actual SQL execution it currently lacks.
  - **`/scripts/{schema}.ts` is in the same boat** (§5, §7): it is
    registered directly on `srv.Router`, not the jwt-wrapped sub-router, so
    it requires no per-request authentication either — only the
    deployment-time `SW_ENABLE_TS_SCHEMAS` env var gates it. When enabled,
    it hands out the full introspected schema (table/column names, types,
    constraint names, relationship graph) to anyone who can reach the port.
    `/_schema` (§3, on `pg2.go:324-333`) is better off but still coarse: it
    *is* behind `jwtMiddleware`, so it requires a valid JWT with a role
    claim, but the handler does no role-specific filtering — any
    authenticated identity, regardless of role, sees the entire schema for
    every exposed table. A rewrite should decide deliberately whether schema
    introspection is a privileged, role-filtered thing or an intentionally
    public one, rather than inheriting three different levels of exposure by
    accident (fully open / any-authenticated-user / DB-role-enforced).
- **Two TypeScript generators, only one wired to the live route, but they
  share code** (§5): `pg/typescript-output.go`'s `OutputTypescriptSchema`
  (plain-object `export const X = {...}` style) is the one
  `pg_typescript.go`'s route actually calls. `pg/typescript.go`'s
  `WriteTypescriptFile`/`WriteTypescriptModelClass`/`WriteTableSerializer`/
  `WriteTypescriptRelations`/`WriteFunctionBody` (ES `class` + decorator
  style, targeting `@salesway/pgts`) are confirmed via `grep` to have zero
  callers anywhere in the tree — genuinely orphaned, from an earlier design.
  But `pg/typescript.go` also defines `TypescriptWriter`, `SerializerFor`,
  `BaseSerializerFor`, and `TypescriptName`, which *are* used by the live
  generator (`ø.SerializerFor(...)` calls throughout
  `pg/typescript-output.go`) — so the file cannot simply be deleted
  wholesale when porting forward; only the class-emitting half is dead.
- **`getenvOrDefault`'s docstring claims Docker-secret-file support**
  (`env.go:8-11`, "It also checks for secrets potentially given to a docker
  container in `/var/run/secrets/<var>.txt`") **but the implementation does
  not do this** (`env.go:12-24`) — stale/aspirational comment; don't port the
  claim without also porting (or dropping) the behavior.
- **Inconsistent anon-role env var defaults**: `DB_ANON_ROLE`
  (`env.go:39`) defaults to `""`, while `websocket-session.go:65` reads the
  *same* `PGRST_DB_ANON_ROLE` variable independently and defaults it to
  `"@unauthenticated"`. Two sources of truth for one concept; the rewrite
  should have exactly one.
- **`PgReloadTables` on introspection failure nils out the schema cache**
  (`sw/defs.go:108-113` — Go assigns all return values including on the
  `error` path of `pg.ReloadTables`, and that function returns `nil, nil,
  err` on any query/scan failure, `pg/types.go:214,348,451`). A transient
  Postgres hiccup during a `SIGUSR1`-triggered reload can wipe `srv.Tables`/
  `srv.Functions` to `nil` for the remainder of the process's life (every
  subsequent `/rel`, `/_schema`, `/scripts/*.ts` request would then operate
  on an empty schema) rather than keeping the last-known-good schema. Worth
  making reload atomic/only-swap-on-success in the rewrite.
- **SQL text is echoed back into error responses** (`pg2.go:77,94,120,125`)
  — convenient for local debugging but a potential information-disclosure
  surface (schema/column names, generated CTE structure) if ever exposed
  without the `SW_ENABLE_DEBUG` gate; currently it's *not* gated by
  `DEBUG_ENABLED` at all (only the `X-Db-Role` header and stack traces are).
- **No explicit pgxpool tuning surface** — pool size/timeouts must be
  encoded in the connection string's query parameters; there's no
  `SW_`-prefixed equivalent of PostgREST's old `db-pool` setting. Since
  PostgREST is already gone from this deployment (§1) and the Go server is
  the *only* thing pooling connections now, the rewrite should decide on an
  explicit, documented pool configuration story rather than relying on
  implicit `pgx` connection-string parameters.
- **`PGRST_DB_SCHEMA` parsing has no whitespace trimming** (`main.go:85`,
  detailed in §2's table) — a comma-separated value with spaces silently
  creates a schema name that never matches anything, quietly dropping that
  schema's exports rather than erroring. The rewrite should trim, and
  probably should also warn/fail loudly on schema names that don't exist in
  `pg_namespace` rather than loading-but-not-exporting them silently.
- **Go server port is hardcoded to `"3001"`** (`main.go:107`) with no env
  var override, unlike PostgREST's old configurable `PGRST_SERVER_PORT`
  (§1) — fine for a single fixed deployment topology, but worth making
  configurable in the rewrite regardless, now that this one process is the
  entire backend and needs to fit varying port layouts on its own.
- **Startup DB-down handling is a hard failure, not a retry-forever
  supervisor pattern**: after 5×5s failed `Acquire` attempts the process
  simply `return`s from `main()` (`main.go:133-136`) without `os.Exit(1)` —
  the process exits 0, which could mask failures from process supervisors
  that only restart on non-zero exit. Worth an explicit exit code in the
  rewrite.
- **`pg3.go`'s debug `pp.Println(ops)`** (`pg3.go:69`) and the `os.Stdout.
  WriteString(request + "\n")` in `pg2.go:211` are raw stdout debug prints
  left in production code paths, unconditional on `DEBUG_ENABLED` — logging
  hygiene to clean up.
- **`.pat` template toolchain provenance is unclear**: nothing in the
  reviewed files indicates what generates `.go` from `.pat` (no `go:generate`
  directive found in the files read, no Makefile target reviewed) — before
  porting the `templates`/`pg` generation approach forward, locate the
  generator tool itself (likely a separate small binary/dependency named
  something like the sigil scheme suggests, e.g. a Pat/Patom-style template
  compiler) so the rewrite either keeps using it or deliberately replaces it
  with `text/template`, `jennifer`, or hand-written builders.
- **Composite types double as "tables"** in `DBAllTables` (`is_real_table:
  false`, `pg/types.go:210-346,324-344`) purely so function argument/return
  composite types get column metadata for computed-column detection — a
  reasonable trick, but worth naming explicitly (e.g. a `Kind` enum) rather
  than overloading the table map in a from-scratch rewrite.
