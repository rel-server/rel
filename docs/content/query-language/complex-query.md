---
icon: material/format-list-checks
---

# Complex queries

Wrapping a query in `{"query": <Relation | WellKnownQuery>, ...}` — a `ComplexQuery` — is how you
write data back (`data`), but the same wrapper also carries five independent flags that shape
what the response carries beyond the plain selection, on a read as much as a write:

```ts
interface ComplexQuery {
  query: Relation | WellKnownQuery
  data?: any        // present : write ; absent : read
  returns?: "none" | "results"
  count?: boolean
  stats?: boolean
  query_plan?: boolean
  sql?: boolean
  rollback?: boolean
}
```

None of these flags change what `query` selects — they only add information alongside it, or
change whether the selection itself is sent back at all.

## No flag set : nothing changes

```json
{ "query": { "relation": "properties", "schema": "hotel", "select": "*" } }
```

responds with the exact same bare array/object it always has. The moment any flag below is
requested, the response becomes an envelope object instead:

```ts
interface ComplexResult {
  result?: any               // the plain selection ; absent iff returns == "none"
  count?: number
  offset?: number
  limit?: number
  stats?: Stat[]
  query_plan?: PlanResult[]
  sql?: SqlResult[]
}
```

`result` always comes last conceptually, but every other key you asked for is filled in before
it — none of them depend on the selection itself.

## `returns`

`"none"` skips sending `result` back. On a write, it also skips the read-back query entirely —
the real performance win, since rel doesn't even ask Postgres to hand back the rows it just
wrote. On a read, the query still runs (a function or view in `select`/`where` may have side
effects worth keeping), only the row data isn't sent to you.

`returns: "none"` with every other flag left off is rejected (`QUERY_UNSUPPORTED_RETURNS`) —
there'd be nothing left to put in the envelope.

## `count`

Read-only — combining it with `data` is `QUERY_COUNT_IS_READ_ONLY`. Reports the total number of
rows your `where`/joins match, ignoring your own `limit`/`offset`, alongside the paginated
`result`:

```json
{
  "query": { "relation": "properties", "schema": "hotel", "select": "*", "limit": 20 },
  "count": true
}
```

```json
{ "result": [ /* 20 rows */ ], "count": 137, "offset": 0, "limit": 20 }
```

`offset` echoes your own (`0` if you didn't set one) ; `limit` is absent if you didn't set one
either.

## `stats`

Write-only — without `data` it's `QUERY_STATS_IS_WRITE_ONLY`. Reports how many rows each
writable relation your query touched actually inserted/updated/deleted:

```json
{
  "query": {
    "relation": "properties", "schema": "hotel",
    "join": { "room_types": { "relation": "room_types", "schema": "hotel", "on": { "property_id": "id" } } },
    "select": "*"
  },
  "data": { "id": 1, "name": "Marina Bay Grand Hotel", "room_types": [{ "id": 5, "base_price": "229.00" }] },
  "stats": true
}
```

```json
{
  "result": { "...": "..." },
  "stats": [
    { "path": [], "table": "hotel.properties", "submitted": 1, "inserted": 0, "updated": 1, "deleted": 0 },
    { "path": ["room_types"], "table": "hotel.room_types", "submitted": 1, "inserted": 0, "updated": 1, "deleted": 0 }
  ]
}
```

`path` is the chain of `join` keys from the root down to that relation — `[]` for the root
itself. A relation marked `write_mode: "readonly"` (or nested under one) never appears here,
same as it never gets written to.

## `query_plan`

Valid on a read or a write. On a read, it's `EXPLAIN`'s own JSON output for the compiled
`SELECT`, without running it beyond what `EXPLAIN` itself does. On a write, every statement
the [Write order](writing.md#write-order) actually runs is captured with `EXPLAIN ANALYZE`
instead — real execution, real numbers — since a later statement's plan only makes sense once
every earlier one has actually run. `query_plan` on a write always executes for real, `rollback`
or not ; pair the two if you want the numbers without keeping the data.

`stats` and `query_plan` can't both be requested on the same write (`QUERY_STATS_QUERY_PLAN_CONFLICT`)
— `EXPLAIN ANALYZE`'s output is a plan, not a row count, so there's nothing left for `stats` to
read once `query_plan` takes over a statement.

## `sql`

Valid on a read or a write, and free either way — it never adds a database round trip, just
hands back the SQL text rel was already going to run:

```json
{ "query": { "relation": "properties", "schema": "hotel", "select": "*" }, "sql": true }
```

```json
{ "result": [ /* ... */ ], "sql": [ { "path": [], "select": "select row_to_json(t1) from ( select ... ) t1" } ] }
```

A write's `sql` includes one entry per relation actually touched (`insert`/`update`/`upsert`/
`delete`, whichever it ran), plus a final entry for the read-back `select`.

## `rollback`

Runs this query inside its own savepoint, rolled back immediately after it executes — useful for
a side-effecting function/view (a read) or a dry run you want the numbers for but not the data
(a write, paired with `stats`, `sql`, or `query_plan`). Nothing else in the same
[batch](batching.md) is affected. `result`/`stats`/`query_plan` still report what actually
happened, right up until the rollback undid it.

`rollback` on its own never turns on the envelope — pair it with another flag (or leave `returns`
at its default) to see anything besides the plain, since-undone selection.

## Availability

`count`, `stats`, `query_plan`, `sql`, and `rollback` are each gated behind a server-wide switch
(`pg.query.allow_count`, `pg.query.allow_stats`, `pg.query.allow_query_plan`, `pg.query.allow_sql`,
`pg.query.allow_rollback` — see
[Configuration reference](../configuration/reference.md)), all on together under `dev: true`.
Asking for a disabled flag isn't an error : `count`/`stats`/`query_plan`/`sql` just come back
empty/absent, `result` and every other, enabled flag in the same request still work normally. The
one exception is `rollback` — since it decides whether data persists, an ungranted request for it
is a hard `QUERY_ROLLBACK_NOT_GRANTED` error instead of a silent no-op.

## On `GET /rel`

[`GET /rel`](get-requests.md) is convenience-only, read-only — it never accepts a `ComplexQuery`
wrapper at all, with or without `data` (`QUERY_COMPLEX_NOT_ALLOWED_ON_GET`). Use `POST` for any
of the flags above.
