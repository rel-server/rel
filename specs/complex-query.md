# Complex queries

`WriteQuery` (`typescript/query.ts`) is replaced by `ComplexQuery` : the same `{query, data}` wrapper, with `data` now optional and five independent flags controlling what the response carries beyond the plain selection.

```ts
export interface ComplexQuery {
  query: RelationQuery | WellKnownQuery
  data?: unknown
  returns?: "none" | "results"
  count?: boolean
  stats?: boolean
  query_plan?: boolean
  sql?: boolean
  rollback?: boolean
}

export type Query = ComplexQuery | RelationQuery | WellKnownQuery | Query[]
```

`data`'s presence, not a separate discriminant, decides whether `query` is written to : present, `query` is written per [Writing data back](../docs/content/query-language/writing.md) ; absent, `query` is read exactly as a bare `RelationQuery`/`WellKnownQuery` would be, with `returns` and the flags below still able to shape the response.

## Parsing

An object with a `query` key decodes to `ComplexQuery` regardless of whether `data` is present. `rawWriteQuery`'s current `query: a WriteQuery needs "data"` rejection (`node_parse.go:114`) is removed : a `ComplexQuery` with no `data` is a valid read. No new field is needed to tell "no data" from an explicit JSON `null` — `Data` already stays `nil` (its zero value) when the key is absent, versus a non-nil `[]byte("null")` when `d.Raw()` is called for an explicit `null`.

`rawWriteQuery` gains `Returns string`, `Count bool`, `Stats bool`, `QueryPlan bool`, `Sql bool`, and `Rollback bool`, decoded off the same object as `Query`/`WellKnown`/`Data` — `Returns` via `StrictString`, the rest via `StrictBool`, per AGENTS.md's JSON-type-detection rule. An unrecognized `Returns` value is a `QUERY_INVALID_RETURNS` error.

A bare `wellknown` object (no wrapping `query` key) never takes `data`, `returns`, or any flag directly — the existing rule (`node_parse.go:94-97`) is unchanged ; wrap it in `{"query": {"wellknown": ...}, ...}` to use any of these.

## Response shape

No flag set (`returns` absent or `"results"`, and `count`/`stats`/`query_plan`/`sql` all false/absent) : the response is the bare selection, exactly as today — no envelope, no back-compat break.

Any flag set : the response is an envelope object :

```ts
interface ComplexResult {
  result?: unknown        // the plain selection ; absent iff returns == "none"
  count?: number           // present iff count == true
  offset?: number           // present iff count == true, echoes the query's own offset (0 if unset)
  limit?: number             // present iff count == true, echoes the query's own limit (absent if unset)
  stats?: Stat[]               // present iff stats == true
  query_plan?: PlanResult[]     // present iff query_plan == true
  sql?: SqlResult[]               // present iff sql == true
}
```

`returns: "none"` with every other flag false/absent is a `QUERY_UNSUPPORTED_RETURNS` error — an envelope with nothing in it is never a valid request. This check considers only `count`/`stats`/`query_plan`/`sql` ; `rollback` is an execution modifier, not a response-shape flag, and never rescues a `returns: "none"`-only request from this error nor changes the bare-response condition above on its own.

On a write, `returns: "none"` skips compiling and running the read-back `SELECT` entirely, rather than running it and discarding the rows.

On a read, `returns: "none"` still compiles and executes the `SELECT` — only streaming `result` is suppressed. A side-effecting read (one calling a function or view with side effects) still produces those effects ; pairing `returns: "none"` with `rollback: true` on a read is how a caller triggers the side effect while discarding both the row data and the effect itself.

A `Sequence` item's envelope is independent of its siblings' — each item is still streamed to its own slot exactly as today (`server/rel.go:343-361`), only its own slot's shape changes.

`streamItem`/`streamRows` (`server/rel.go:383-414`) write JSON incrementally straight from `pgx.Rows`, never buffering a result set — the existing "once streaming starts there's no clean error envelope left to fall back to" invariant (`rel.go:308`'s comment). Every non-`result` envelope key (`stats`, `count`, `sql`, `query_plan`) is knowable before read-back streaming begins (write phase runs first ; `count` is its own prior query ; `sql`/`query_plan` resolve during compile/write), so they are always emitted before `result` in the envelope, computed upfront — `result` itself stays streamed row-by-row, never buffered into memory to be `json.Marshal`ed as a whole.

> **Why:** an envelope only when needed: a caller that never asks for `count`/`stats`/`query_plan`/`sql` keeps parsing the same bare array/object it always has ; no client code breaks on upgrade.

## `count`

Valid only on a read (`data` absent) — a write's read-back selects exactly the rows the write just touched, not an arbitrary page of a larger relation, so a "total across the whole relation" figure doesn't correspond to anything meaningful there. Requesting `count` alongside `data` is a `QUERY_COUNT_IS_READ_ONLY` error.

Runs the query's own `where`/joins a second time — same predicate, no `select`, no `order_by`, no `limit`/`offset` — as `select count(*) from (<query's FROM/WHERE/JOINs>) t`, and reports that alongside the already-paginated `result`.

> **Why:** a second query instead of `count(*) over()`: a window-function count forces Postgres to materialize every matching row before `limit` can apply, discarding whatever index-driven top-N shortcut the plain paginated query would otherwise get — for the common case (a large table, a small page), that's far more expensive than evaluating the predicate twice. A second, `LIMIT`-free query lets Postgres plan the count on its own, sometimes as an index-only scan, and pays only for evaluating the predicate again — not for building/sorting/serializing rows it already built once.

`count` is not exact-vs-estimated configurable in this version — always the real `count(*)`. An approximate variant (`pg_class.reltuples`-based) is out of scope here.

`count` compiles through a distinct `CompileCount` function in `query/sql.go`, a sibling to `CompileSelect`, rather than a textual wrapper around the already-compiled `SELECT` — it emits the same `FROM`/`JOIN`/`WHERE` (a root's `where` can reference join keys) without `LIMIT`/`OFFSET`/`ORDER BY`/the JSON projection.

## `stats`

Valid only on a write (`data` present) ; requesting `stats` without `data` is a `QUERY_STATS_IS_WRITE_ONLY` error.

```ts
export interface Stat {
  path: string[]       // join-alias path ; [] for the root relation itself
  table: string        // fully-qualified relation name, "schema.table"
  submitted: number      // rows denormalized for this node (query/write.go's denormalize), written or not
  inserted: number
  updated: number
  deleted: number
}
```

One `Stat` per writable node touched by the write (a `READONLY` node, or anything nested under one, is excluded — same pruning `assignNodeIDs` already does). `path` is the chain of `join` keys from the root down to this node ; a flat array keyed by path avoids the collision a nested object would risk when a relation happens to be joined under an alias literally named `inserted`/`updated`/`deleted`.

Every DML statement (`query/write_dml.go`'s `runInsert`/`runUpdate`/`runUpsert`/`runDelete`) currently discards its `pgconn.CommandTag` ; each keeps it and reports `RowsAffected()` under the matching counter. `_data`-only housekeeping statements (`recoverKeys`, the trailing `update _data set keys = ...` in `runUpsert`/`runUpdate`) are never counted — only statements against the actual relation being written.

`runUpsert` compiles one `INSERT ... ON CONFLICT DO UPDATE` — one `CommandTag`, which can't tell an insert from an update on its own. Its `dml` CTE's `RETURNING` clause gains `(xmax = 0) AS inserted` ; `inserted`/`updated` are split using that boolean instead of reading a single combined `RowsAffected()`.

Requesting `stats` together with `query_plan` on the same write is a `QUERY_STATS_QUERY_PLAN_CONFLICT` error.

> **Why:** wrapping a DML statement in `EXPLAIN (ANALYZE, FORMAT JSON)` (## query_plan) replaces its output with plan rows — neither `RowsAffected()` nor a `RETURNING`-based split (`runUpsert`'s `xmax` trick) has anything left to read from, so the two flags can't both be satisfied on the same write.

## `query_plan`

Valid on both reads and writes.

```ts
export interface PlanResult {
  path: string[]       // join-alias path ; [] for the root relation itself
  select?: unknown
  insert?: unknown
  update?: unknown
  upsert?: unknown
  delete?: unknown
}
```

Same per-node shape as `SqlResult` below, one plan per statement kind that node actually compiles — a caller correlates a plan to its statement by `path` plus which field is set.

On a read : `EXPLAIN (FORMAT JSON)` of the compiled `SELECT`, not executed for real beyond what `EXPLAIN` itself runs — `query_plan: PlanResult[]` holds a single `[{path: [], select: ...}]`.

On a write : each DML statement's plan is only meaningful once every statement before it in the Writing Algorithm's order (`docs/content/query-language/writing.md ## Write order`) has actually run — `_data` and any generated ids a later statement depends on don't exist until the earlier statements have executed. `EXPLAIN` alone (no execution) is therefore unusable for anything past the first write statement ; every DML statement instead runs as `EXPLAIN (ANALYZE, FORMAT JSON)`, for real, in its normal position in the Writing Algorithm. `query_plan: PlanResult[]` holds one entry per node touched, its plan(s) under whichever field(s) that node actually ran, plus a final entry for the read-back `SELECT`. `COPY` (loading `_data`) has no plan and contributes no entry.

A write's `query_plan` executes for real regardless of `rollback` ; pair the two explicitly to get plans without persisting.

> **Why:** EXPLAIN ANALYZE and not a two-pass compile-then-explain: the Writing Algorithm's statements are stateful — each one's plan depends on rows only the previous statement created — so there's no way to get a write statement's real plan without having actually run everything before it.

`runUpdate` folds its `_data`-keys update into the same statement as its main `UPDATE` (a trailing CTE consuming `RETURNING`, mirroring `runUpsert`), instead of reading `RETURNING` rows back in Go to issue per-row `update _data set keys` calls — this keeps it safe to wrap in `EXPLAIN ANALYZE` for `query_plan`.

## `sql`

Valid on both reads and writes ; never executes anything beyond what the request would already run without `sql` set — asking for it adds zero DB round trips.

```ts
export interface SqlResult {
  path: string[]        // join-alias path ; [] for the root relation itself
  select?: string
  insert?: string
  update?: string
  upsert?: string          // runUpsert compiles one combined INSERT ... ON CONFLICT DO UPDATE
  delete?: string
}
```

One `SqlResult` per node touched, holding the text of whichever statement kind(s) that node actually compiled — a `merge`-mode node might carry both `insert` and `delete`, an `upsert`-mode node carries `upsert` alone (never split into separate `insert`/`update` text, since `runUpsert` only ever compiles the one combined statement), and a plain read's single entry carries only `select` with `path: []`. Every DML/`SELECT` statement's text is fully determined by the resolved query tree (node structure, columns, write mode) — `query/write_dml.go`'s CTEs reference `_data` by `__node_id`, never by literal submitted values, which stay separate positional args. `sql: SqlResult[]` is the already-compiled `writer.SQLWriter.String()` of each statement `handleRel` was going to run anyway (`server/rel.go:310-336`'s existing compile step, for a write plus the read-back ; the compiled `SELECT` alone, for a read), collected instead of only executed.

> **Why:** this is free: nothing about generating a statement's SQL text depends on the actual submitted values or on `_data` being populated — only on the query tree's shape, which is already fully resolved before any DML runs.

## `rollback`

Valid both on read and write, to account for possible side-effects some select could produce in views / functions.

The request's overall transaction (`server/rel.go:275`, one `begin`/`commit` around every item in a `Sequence`) is unchanged. An item with `rollback: true` runs inside its own `SAVEPOINT`, issued immediately before that item's own execution and rolled back to immediately after — for a write, around its `ExecuteWriteStateParams` call ; for a read, around whatever `streamItem` executes (the compiled `SELECT`, which may call a side-effecting function or view). Only that item's own effects are undone, leaving every other item in the same `Sequence` (and the request's eventual commit) unaffected. An item's own `result`/`stats`/`query_plan` still reflect what actually happened before the rollback ; only the data itself doesn't persist.

Savepoint names are unique per item within a `Sequence` (`sp0`, `sp1`, ...) — never reused, even across items that don't themselves request `rollback`. `WriteState`'s `nextNodeID`/`nextRowID` counters (`query/write.go`) do not rewind when a savepoint rolls back : the `_data` rows a rolled-back item produced are undone by the savepoint itself, so the counters simply burn id space rather than causing any collision or leak.

A rolled-back write's read-back `SELECT` runs (and its rows are captured) before `ROLLBACK TO SAVEPOINT`, not after — the savepoint wraps the write's own `_data` population too, so both the real table rows and this item's own `_data` rows are gone the instant the rollback happens ; the already-captured rows are what `result` streams from afterward, once the item reaches its normal place in the response.

`rollback` never triggers the envelope on its own and contributes no envelope key ; it only changes whether an item's effects persist. Pairing it with `returns: "results"` (to see rolled-back data), `stats`, `sql`, or `query_plan` produces a dry run with actual content in the envelope.

A `GET /rel` request accepts only a bare `RelationQuery` or `WellKnownQuery` ; a `ComplexQuery` wrapper, with or without `data`, is a `QUERY_COMPLEX_NOT_ALLOWED_ON_GET` error on `GET`.

## Impacts

- `typescript/query.ts` : `WriteQuery` → `ComplexQuery`, `Query` union updated, `Stat`/`ComplexResult` types added.
- `query/node_parse.go` : `data`-required assertion removed from the `query`-key branch ; `rawWriteQuery` distinguishes "no data" from "empty data", and gains `Returns`/`Count`/`Stats`/`QueryPlan`/`Sql`/`Rollback` fields.
- `query/sql.go` : `CompileCount`, a sibling to `CompileSelect`, added for `count`.
- `query/write.go` / `query/write_dml.go` : `WriteResult` gains per-node `Stat` accumulation ; `runUpsert`'s `RETURNING` clause gains the `xmax = 0` column ; `runUpdate` is restructured to fold its `_data`-keys update into its main `UPDATE` statement, like `runUpsert` does, instead of issuing it as separate per-row Go-side statements.
- `server/rel.go` : `handleRel` gains the envelope-wrapping response path, the `count` second query, the `query_plan`/`sql` collection, the per-item `SAVEPOINT` handling for `rollback`, and a `GET`-only rejection of any `ComplexQuery` wrapper.
- `errcode` : `QueryUnsupportedReturns`, `QueryCountIsReadOnly`, `QueryStatsIsWriteOnly`, `QueryComplexNotAllowedOnGet`, `QueryInvalidReturns`, `QueryRollbackNotGranted`, `QueryStatsQueryPlanConflict` added alongside the existing `Query compile errors` group.
- Docs : `docs/content/query-language/writing.md`, `reference.md`, `typescript-client.md` all reference `WriteQuery` today and need updating to `ComplexQuery` plus a description of the new flags.
- Tests : `query/write_test.go`, `query/node_parse_test.go`, `server/rel_wellknown_test.go` all construct `WriteQuery`-shaped JSON directly and need covering for the new optional-`data`, flag, and envelope paths.
- `config.Config` : gains `allow_count`/`allow_stats`/`allow_query_plan`/`allow_sql`/`allow_rollback` (## Availability), each server-wide, defaulting on under `cfg.Dev`.
- `server/rel.go`'s well-known branch (`resolveWellKnownItem`, `item.precompiledRead` at `rel.go:318`) is a separate compile path from the plain-relation branch — `count`'s compile variant and `sql`/`query_plan` collection need their own hook there too, since `ComplexQuery.query` allows a `WellKnownQuery`.

## Availability

`count`, `stats`, `query_plan`, `sql`, and `rollback` are each gated behind their own server-wide config flag — `allow_count`, `allow_stats`, `allow_query_plan`, `allow_sql`, `allow_rollback` — mirroring `ComplexQuery`'s own field names, not a per-role grant. `cfg.Dev` always turns all five on.

`count`/`stats`/`query_plan`/`sql` expose predicate-evaluation cost, write outcome, or schema/index shape ; a request for one of these four while its flag is off is not an error : that flag's own envelope key is omitted (an empty array for the array-typed `stats`/`query_plan`/`sql`, an absent `count` — `offset`/`limit` follow `count`'s own presence, so they're absent too) while `result` and every other, enabled flag in the same request still return normally.

> **Why:** not reject the request outright: a disabled flag is a capability gap, not a malformed request — a client talking to a server that later turns a flag off should degrade, not start erroring on requests it was already sending.

`rollback` while `allow_rollback` is off is a `QUERY_ROLLBACK_NOT_GRANTED` error instead of a silent degrade — unlike the other four, `rollback` changes whether data persists, so silently ignoring it would let effects persist that the caller believed were undone.

`allow_count`/`allow_stats`/`allow_query_plan`/`allow_sql`/`allow_rollback` are new top-level `config.Config` fields, alongside `cfg.Dev` — no per-role config surface is needed for them.
