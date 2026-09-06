# Well Known Queries

- rel reads and compiles them from disk on startup, and again on every `SIGUSR1` reload (`boot/reload.go`'s 7-step sequence, the same reload that reintrospects the schema and rebuilds the mux). There is no separate `SIGUSR2` trigger : a well-known reload is always a full, clean re-read from disk and recompile, never an incremental diff.

## Configuration

>: What other configuration options would be relevant ?

Consumed the same way `http.static.path` already is : the config field itself (`config.Pg.Query.WellKnownDirs`) stays the raw, unsplit string ; splitting on `:` happens at the point of use (mirroring `static/static.go`'s `strings.Split(cfg.Static.Path, ":")`). A directory in the list that doesn't exist is silently skipped, same rule `http.static.path` documents. No cap on recursion depth or file count while walking the directories.

## Behaviour

Every format (`.json`/`.yml`/`.yaml`/`.huml`) is converted to a plain JSON value tree before parsing — `sonic/ast`'s existing JSON-specific parser (`query.ParseExpression`/`ParseQuery`) is reused unchanged for all of them.

A well-known query is invoked exactly like a `Relation` would be — bare (a read) or wrapped in `WriteQuery.query` (a write) — and can be freely mixed with plain `Relation` items in a `Query[]` sequence, sharing that sequence's transaction like anything else in it. See `## Querying` below for the wire shape. They are evaluated once and their statements are prepared, ready to be queried for maximum performance.

"Prepared" doesn't mean issuing a literal Postgres `PREPARE` — pgx already caches statement plans per connection on its own (`QueryExecModeCacheStatement`), and a prepared statement doesn't survive across pooled connections. It means rel compiles the query to SQL text exactly once, at load time, and reuses that same text for every subsequent invocation instead of recompiling per request.

The write side compiles once too. `runInsert`/`runUpsert`/`runUpdate`/`runDelete` build their SQL text purely from the resolved `QueryNode` tree's static shape (columns, `write_mode`, `on_conflict`) and each node's own precomputed `__node_id` (`dc.ids[node]`, baked in as a literal constant — e.g. `where tmp.__node_id = 3`), never from the request's actual payload values. Denormalizing the request's JSON into `_data` is the only genuinely per-request step.

Node-ID assignment staying stable across invocations depends on always starting from a fresh `WriteState{}`, offset 0 — which no longer holds unconditionally now that a well-known write can sit anywhere in a larger `Query[]` sequence and get offset node IDs assigned at request time. Node IDs are resolved fresh per request today (`resolveWellKnownItem`'s `paramValues`/`writer.SQLWriter.ResolveArgs` threading, `server/rel.go`), so this is functionally fine as-is ; the write-side compile-once split (baking `__node_id` in as a load-time literal) will need to account for a non-zero starting offset once built — see `TODO.md`.

The statement list is not uniformly "the same on every invocation," though the SQL text each statement compiles to still is. `dmlCompiler.phase1`/`phase2` (`write_dml.go`) skip a node's statement when the payload didn't populate it, computed once per request from `denormalize`'s own output right after it runs :

- **phase1 (insert/update/upsert)** skips a node — and its whole subtree — once the node itself has zero `_data` rows. Payload nesting guarantees this is safe : a descendant's data can only ever exist nested inside this node's own JSON value, so an unpopulated node's descendants are unpopulated too.
- **phase2 (delete)** is gated on the *parent's* population, never the node's own. A delete-bearing node with zero `_data` rows of its own means "nothing survived in the payload under this parent, delete everything that used to be here." The root (no parent) is never skipped this way, matching how an empty top-level payload already means "delete everything matching `where`" today.

`write_dml.go`'s `run*` functions currently compile and execute their SQL in the same call — no split today between "compile this tree's DML once" (cacheable, the well-known-query part) and "run the already-compiled statements against this request's own `_data` rows, skipping the ones the populated-check rules out" (per-request). Reusing a well-known write query's compiled statements across requests needs that split built first.

## Definition

A well-known file contains either one `WellKnownQuery` or `WellKnownQuery[]`. It defines a query in the Rel JSON Query Language ; the only difference is that it declares parameters that can then be used by the query with `["$param", param_name: string, optional_cast?: string]`.

```typescript
interface WellKnownQuery {
  name: string
  params: {
    [name: string]: WellKnownParam
  }
  query: QueryNode
}

interface WellKnownParam {
  type?: string // any postgres type that the json value will then cast to - unless if it is JSON since it will be JSON by default
  default?: unknown // must be of the specified type or castable. Can be null to indicate non-requiredness
}
```

`type` and `["$param", name, cast?]`'s own `cast` are two separate settings that do two separate jobs :

- **`type` (declared once, in `params`)** is checked against the caller-supplied JSON value up front, in Go, before the query ever reaches Postgres — a wrong-typed param fails at the request boundary (`WELL_KNOWN_PARAM_TYPE_MISMATCH`), not as a Postgres cast error surfacing from inside the compiled SQL. The check is shallow : a handful of common type-name spellings bucketed into "must be a JSON number/string/boolean" ; anything else (`type` unset, `jsonb`/`json`, an exotic type name) passes through unchecked.
- **`cast` (written per usage site, inside the query)** is what the SQL placeholder is actually cast to — `$1::text`, say. Left unspecified, it defaults to `::jsonb`.

Nothing currently copies a param's declared `type` into a usage site's `cast` automatically : a param declared `"name": {"type": "text"}`, then used as bare `["$param", "name"]` (no third element) compiles to `$1::jsonb`, not `$1::text`. To get `::text` in the SQL, the query must write it explicitly at each usage site : `["$param", "name", "text"]`.

`default` has three distinct states, requiring presence-aware JSON decoding on the Go side (a plain `map[string]any` lookup can't distinguish "key absent" from "key present with value `null`") : no `default` key at all → required (`WELL_KNOWN_PARAM_REQUIRED` if omitted by the caller) ; `default: null` → optional, defaults to SQL `NULL` ; `default: <value>` → optional, defaults to that value.

`WELL_KNOWN_UNUSED_PARAM`/`WELL_KNOWN_UNKNOWN_PARAM` (unused param / non-existing param reference) come from a tree walk collecting every `ParamExpr.Name` actually referenced, diffed against the declared `params` map's keys — done once, at file-load time, against the cached tree, never re-walked per request. See `## Behaviour` for how `$param` itself compiles (`writer.SQLWriter.BindParam`/`ResolveArgs`).

## Querying

```typescript
interface WellKnownQuery {
  wellknown: string
  params?: {
    [name: string]: unknown
  }
}
```

There's no separate infrastructure to reuse : the same auth pipeline, the same `boot.BuildMux` mount, the same response shape (`/rel`'s manual streaming JSON array + `RelErrorResponse` error envelope) apply, because it's the same handler as a plain `Relation`. A well-known item's statement, when it's a read, is the one already compiled at load time (`## Behaviour`) rather than recompiled per request ; only its args are resolved fresh, per request, against the caller's `params`.

## Compilation Errors

- `WELL_KNOWN_DUPLICATE_NAME` : duplicate well-known name — two well-known queries intended to register the same name.
- `WELL_KNOWN_UNUSED_PARAM` : unused param — the query declares a parameter it doesn't use.
- `WELL_KNOWN_UNKNOWN_PARAM` : non-existing param — a `[$param]` statement calls a non-existing param.

## Execution Errors

- `WELL_KNOWN_UNKNOWN_QUERY` : a request named a query that isn't registered — either it was never defined, or it was deactivated (an invalid definition, or a name collision — see `## Behaviour`). Rejected identically either way ; there is no separate "deactivated" state visible to a caller.
- `WELL_KNOWN_PARAM_TYPE_MISMATCH` : a supplied param was of the wrong type (checked against its declared `type` before the query reaches Postgres — see `## Definition`).
- `WELL_KNOWN_PARAM_REQUIRED` : the user did not specify a param that did not have a default.
