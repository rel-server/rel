# Well-known queries

A well-known query is a named, parameterized query saved to disk and registered under its own
name at startup — a view, but bidirectional, and callable through the same `/rel` endpoint as
everything else:

```json
{ "wellknown": "top_rated_properties", "params": { "min_rating": 4 } }
```

rel compiles every well-known query to SQL once, at startup (and again on a schema reload),
instead of parsing and planning it fresh on every request. It's also exported, fully typed, by
rel's [generated TypeScript client](../typescript-client.md) — callers get real parameter and
result types for a well-known query the same way they would for a hand-written one.

## Defining one

rel reads every `.json`, `.yml`, `.yaml`, and `.huml` file (skipping any name starting with
`_`) under the directories listed in `pg.query.wellknown_path` (default `/wellknown`, a
`:`-separated list — see [Configuration](../configuration/index.md)), recursively. A file's
location under that path is irrelevant; what identifies a well-known query is the `name`
declared inside it.

```yaml
name: top_rated_properties
params:
  min_rating:
    type: integer
    default: 4
query:
  relation: properties
  schema: hotel
  where: [">=", "star_rating", ["$param", "min_rating", "integer"]]
  select: { id: "id", name: "name", star_rating: "star_rating" }
```

A file can declare one query or an array of them (`WellKnownQuery[]`). Two files anywhere
under `wellknown_path` can collide on the same `name` — that's an ordinary error, not
something directory layout can prevent by keeping them apart: rel logs a warning and
deactivates every query registered under that name, not just the newer one. A query with any
other definition error is deactivated the same way. A request for a deactivated or
never-registered name gets the same `WELL_KNOWN_UNKNOWN_QUERY` error either way — nothing
distinguishes "never existed" from "failed to load" to a caller.

Well-known queries reload the same way the schema itself does: send the process `SIGUSR1`, and
every file under `wellknown_path` is re-read and recompiled from scratch.

## Declaring and using parameters

`["$param", "min_rating", "integer"]` above is how a well-known query's `query` tree reaches
its own declared parameters. Each entry in `params` can set:

- **`type`** — checked against the caller's supplied JSON value before the query ever reaches
  Postgres. A `min_rating` declared `type: integer` rejects a caller passing a string, with
  `WELL_KNOWN_PARAM_TYPE_MISMATCH`, before any SQL runs.
- **`default`** — omitted entirely means the param is required (`WELL_KNOWN_PARAM_REQUIRED` if
  the caller doesn't supply it); `default: null` means optional, defaulting to SQL `NULL`;
  any other `default` value is used as-is when the caller omits the param.

`type` only gates what's accepted at the door — it doesn't automatically become the SQL cast.
The third element of `["$param", name, cast]` sets that separately, and defaults to `::jsonb`
when left off. Write it explicitly at each usage site to get the SQL type you actually want:
`["$param", "min_rating", "integer"]` compiles to a `$1::integer` placeholder; a bare
`["$param", "min_rating"]` would compile to `$1::jsonb` instead, even though `type: integer`
was declared above.

## Calling one over `POST /rel`

A well-known query occupies exactly the slot a plain `Relation` would, in either position: bare
for a read, wrapped in `WriteQuery.query` for a write.

```json
POST /rel
{ "wellknown": "top_rated_properties", "params": { "min_rating": 4 } }
```

```json
POST /rel
{
  "query": { "wellknown": "checkin_guest" },
  "data": { "booking_id": "b1a2c3d4-...", "status": "checked_in" }
}
```

## Calling one over `GET /rel`

The same query is reachable with a plain `GET`, `wellknown` and each param as its own
`params.<name>=` key:

```
GET /rel?wellknown=top_rated_properties&params.min_rating=4
```

Every `params.<name>` value is decoded as a string first, then opportunistically re-parsed as
JSON — `params.min_rating=4` becomes the number `4`, `params.active=true` becomes the boolean
`true`, and anything that doesn't parse as JSON stays a plain string. That means a text-typed
param whose value happens to look like a number or boolean needs to be quoted explicitly to
survive as a string: `params.code=12345` decodes to the number `12345`, while
`params.code=%2212345%22` (URL-encoded `"12345"`) decodes to the string `"12345"`. The same
escape hatch handles a structured value — `params.tags=%5B1%2C2%2C3%5D` (URL-encoded `[1,2,3]`)
decodes to the array `[1, 2, 3]`; there's no `params.tags.0=`/`params.tags.1=` nesting form for
building one up piece by piece. `GET /rel` is read-only, so a well-known write query only ever
runs over `POST`.

## Mixing with other queries

A well-known query composes freely with plain relations inside a [batched
request](batching.md) — both share the same transaction as anything else in the batch:

```json
[
  {
    "query": { "wellknown": "checkin_guest" },
    "data": { "booking_id": "b1a2c3d4-...", "status": "checked_in" }
  },
  { "relation": "rooms", "schema": "hotel", "where": ["=", "id", 42], "select": "*" }
]
```
