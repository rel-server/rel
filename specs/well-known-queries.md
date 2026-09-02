# Well Known Queries

They're basically what views achieve in SQL, but in rel, with the write operations planned for.

- rel reads and compiles them from disk on startup, and again on every `SIGUSR1` reload — the
  same reload that reintrospects the schema and rebuilds the mux (`boot/reload.go`'s existing
  7-step sequence). No separate `SIGUSR2` trigger : a well-known reload is always a full,
  clean re-read from disk and recompile, never an incremental diff, folded into the reload
  rel already has rather than a second mechanism next to it.

## Configuration

* `pg.query.wellknown_path` (default `'/wellknown'`) : a colon `:` separated list of directories containing well-known queries in json or yaml format.

>: What other configuration options would be relevant ?

Consumed the same way `http.static.path` already is : the config field itself
(`config.Pg.Query.WellKnownDirs`) stays the raw, unsplit string ; splitting on `:` happens at
the point of use, not in the config loader (mirroring `static/static.go`'s
`strings.Split(cfg.Static.Path, ":")`). A directory in the list that doesn't exist is silently
skipped, same rule `http.static.path` documents — consistent, not a special case. No cap on
recursion depth or file count while walking the directories ; a pathological directory tree is
the deploying developer's own responsibility, same posture the rest of this codebase's
directory-scanning configs take.

## Behaviour

Rel reads the directories of `pg.query.wellknown_path` recursively and considers every `.json`, `.yml`, `.yaml` file whose name doesn't start with `_`. Every format is converted to a plain JSON value tree before parsing — `sonic/ast`'s existing JSON-specific parser (`query.ParseExpression`/`ParseQuery`) is reused unchanged for both ; YAML exists purely for author readability, not because rel treats it as semantically different from JSON.

If a query has an error, a warning is logged and the query is deactivated — it was never validly defined, so it's never queryable, the same way a well-known query wouldn't exist at all if it were never written. If a query introduces a name that collides with an already-loaded one, rel logs a warning and deactivates *every* well-known query registered under that name (not just the newest one) ; a request naming a deactivated (or never-validly-defined) query is rejected the same way a genuinely unknown name would be.

`name` is declared inside the file's own content (`## Definition` below) and is the *only* thing that identifies a well-known query — directory layout under `wellknown_path` is purely an authoring convenience with no bearing on the exposed name. A developer is free to lay out files however they like, including declaring several `WellKnownQuery` entries (via the `WellKnownQuery[]` form) in one file ; two files in unrelated subdirectories can still collide on the same declared `name`, and that's an ordinary collision, not a special case.

Well-known queries are not meant to be mixed-and-matched with other queries ; they're evaluated once and their statements are prepared, ready to be queried for maximum performance.

"Prepared" doesn't mean issuing a literal Postgres `PREPARE` — pgx already caches statement plans per connection on its own (`QueryExecModeCacheStatement`), and reimplementing that under a connection pool (a prepared statement doesn't survive across pooled connections) would be pure overhead for no benefit. It means rel compiles the query to SQL text exactly once, at load time, and reuses that same text for every subsequent invocation instead of recompiling per request.

That compile-once story extends to the write side too, not just reads. `runInsert`/`runUpsert`/`runUpdate`/`runDelete` build their SQL text purely from the resolved `QueryNode` tree's static shape (columns, `write_mode`, `on_conflict`) and each node's own precomputed `__node_id` (`dc.ids[node]`, baked in as a literal constant — e.g. `where tmp.__node_id = 3`) — never from the request's actual payload values. Denormalizing the request's JSON into `_data` is the only genuinely per-request step. (Node-ID assignment staying stable across invocations depends on always starting from a fresh `WriteState{}`, offset 0 — true as long as a well-known query never shares `_data` with another item in a larger sequence, which is exactly what "not meant to be mixed-and-matched" already rules out.)

The statement list itself is *not* uniformly "the same on every invocation" any more, though the SQL text each statement compiles to still is. `dmlCompiler.phase1`/`phase2` (`write_dml.go`) skip a node's statement when the payload didn't populate it, computed once per request from `denormalize`'s own output right after it runs — cheap, and it doesn't touch compiled SQL text at all, so it composes cleanly with the compile-once story rather than working against it :

- **phase1 (insert/update/upsert)** skips a node — and its whole subtree — once the node itself has zero `_data` rows. Payload nesting guarantees this is safe : a descendant's data can only ever exist nested inside this node's own JSON value, so an unpopulated node's descendants are unpopulated too.
- **phase2 (delete)** is gated the other way round : on the *parent's* population, never the node's own. A delete-bearing node with zero `_data` rows of its own is exactly the "nothing survived in the payload under this parent, delete everything that used to be here" signal — not a skip condition. The root (no parent) is never skipped this way, matching how an empty top-level payload already means "delete everything matching `where`" today.

The catch this still leaves open : `write_dml.go`'s `run*` functions currently compile *and* execute their SQL in the same call — there's no split today between "compile this tree's DML once" (cacheable, the well-known-query part) and "run the already-compiled statements against this request's own `_data` rows, skipping the ones the populated-check rules out" (per-request). Reusing a well-known write query's compiled statements across requests needs that split built first. Flagging this as implementation work this feature depends on, not a remaining design question.

The shape of their output is known and exported in typescript.

## Definition

A well-known file contains either one `WellKnownQuery` or `WellKnownQuery[]`. It defines a query in the Rel JSON Query Language ; the only difference is that it declares parameters that can then be used by the query with `["$param", param_name: string, optional_cast?: string]`


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

`type` and `["$param", name, cast?]`'s own `cast` are two SEPARATE settings that currently do two separate jobs, not one governing the other :

- **`type` (declared once, in `params`)** is checked against the caller-supplied JSON value up front, in Go, before the query ever reaches Postgres — a wrong-typed param fails at the request boundary (`WELL_KNOWN_PARAM_TYPE_MISMATCH`), not as a Postgres cast error surfacing from inside the compiled SQL. This check is deliberately shallow : a handful of common type-name spellings bucketed into "must be a JSON number/string/boolean" ; anything else (`type` unset, `jsonb`/`json`, an exotic type name) is passed through unchecked. Not a Postgres type system, just enough to catch the routine mistake.
- **`cast` (written per usage site, inside the query)** is what the SQL placeholder is actually cast to — `$1::text`, say. Left unspecified, it defaults to `::jsonb`.

Nothing currently copies a param's declared `type` into a usage site's `cast` automatically. Concretely : a param declared `"name": {"type": "text"}`, then used as bare `["$param", "name"]` (no third element) compiles to `$1::jsonb`, not `$1::text` — `type` only gated whether the caller's JSON value was a string, it played no role in the SQL. To get `::text` in the SQL, the query has to write it explicitly at each usage site : `["$param", "name", "text"]`. Auto-filling a usage site's missing `cast` from its own param's declared `type` is a reasonable follow-up, not yet built.

`default` has three distinct, confirmed states, which needs presence-aware JSON decoding on the Go side (a plain `map[string]any` lookup can't distinguish "key absent" from "key present with value `null`") : no `default` key at all → required (`WELL_KNOWN_PARAM_REQUIRED` if omitted by the caller) ; `default: null` → optional, defaults to SQL `NULL` ; `default: <value>` → optional, defaults to that value.

`WELL_KNOWN_UNUSED_PARAM`/`WELL_KNOWN_UNKNOWN_PARAM` (unused param / non-existing param reference) come from a tree walk collecting every `ParamExpr.Name` actually referenced, diffed against the declared `params` map's keys — done once, at file-load time, against the cached tree ; never re-walked per request. See `## Behaviour` above for how `$param` itself compiles (`writer.SQLWriter.BindParam`/`ResolveArgs`).

## Querying

Wellknown query are available on the endpoint `/wellknown`, who expects

```typescript
type WellknownQuery {
  name: string
  params?: {
    [name: string]: unknown
  }
  data?: unknown
}
```

`data` should only be supplied for write queries. `POST` and `GET` are available for wellknown just like for `/rel` for easy querying capabilities.

`/wellknown` is, in effect, `/rel` with precompiled queries — it reuses the exact same infrastructure : the same auth pipeline (`jwt.Middleware`'s Verify, then `check_session`/Renew/`SET LOCAL ROLE` in the same order `/rel`/`/rpc` already share), mounted through `boot.BuildMux` alongside `/rel`/`/rpc`/`/static` so `websec.Middleware`'s CORS/CSP and `logging.RequestMiddleware`'s request-id logging apply automatically ; the same response shape (`/rel`'s manual streaming JSON array + `RelErrorResponse` error envelope).

`GET`'s own query-string encoding of this shape — `well-known-queries-get.md` — is a separate document, the same way `query-json.md` is `GET /rel`'s own encoding of `query.ts`'s `Relation` shape ; the two don't share a grammar (`/wellknown` has no query tree in the request, only a name and a flat param bag), so there's nothing to fold into one file.

## Compilation Errors

- `WELL_KNOWN_DUPLICATE_NAME` : duplicate well-known name
  Two well-known queries intended to register the same name
- `WELL_KNOWN_UNUSED_PARAM` : unused param
  The query declares a parameter it doesn't use
- `WELL_KNOWN_UNKNOWN_PARAM` : non-existing param
  A `[$param]` statement calls a non-existing param

Renamed from the original `RW001`/`RW002`/`RW003` draft numbering to match every other
rel-internal error code's `SCREAMING_SNAKE_CASE` convention (`errcode.Code`, delivered through
the same `X-Rel-Errorcode` header/`code` field as everything else) — `RWxxx` didn't fit either
of `specs/error-handling.md`'s two established families (`RSxxx` for a PL/pgSQL author's own
`raise ... using errcode`, `SCREAMING_SNAKE_CASE` for rel-internal), so it would have been a
third, unprecedented scheme.

## Execution Errors

- `WELL_KNOWN_UNKNOWN_QUERY` : a request named a query that isn't registered — either it was
  never defined, or it was deactivated (an invalid definition, or a name collision — see
  `## Behaviour`). Rejected identically either way ; there is no separate "deactivated" state
  visible to a caller.
- `WELL_KNOWN_PARAM_TYPE_MISMATCH` : a supplied param was of the wrong type (checked against
  its declared `type` before the query reaches Postgres — see `## Definition`)
- `WELL_KNOWN_PARAM_REQUIRED` : the user did not specify a param that did not have a default
