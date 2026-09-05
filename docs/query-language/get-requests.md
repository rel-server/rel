# Querying with `GET`

Every query on the preceding pages was a JSON body on `POST /rel`. The same read-only queries
— a relation, joins, `where`, `select`, `order_by`, `limit`/`offset`, `distinct`/`distinct_on`
— are also reachable as a plain `GET /rel` query string, decoding to exactly the same
`Relation` tree. Nothing new is expressible this way; it's a second, URL-friendly spelling of
a subset of what `POST /rel` already accepts, useful for a request you want cacheable, bookmarkable,
or embeddable in a plain `<a href>` — a login-gated report link, say, or a `curl` one-liner.

`GET /rel` is read-only: no `data`, no batching several queries in one request, and
`write_mode`/`on_conflict`/`insert_columns`/`update_columns` are rejected outright, at any
depth. Use [`POST /rel`](writing.md) for anything that writes.

## Structural layer

Every key is a dot-separated path into the same `Relation` shape:

```
GET /rel?relation=properties&schema=hotel
  &where=and(gte(star_rating,4),eq(chain_id,3))
  &select=id,name,star_rating,room_types
  &order_by=-star_rating,name
  &limit=10&offset=0
  &join.room_types.relation=room_types
  &join.room_types.schema=hotel
  &join.room_types.on.property_id=id
```

This decodes to the same tree as the equivalent JSON body — repeat a key to build an array
(`insert_columns=a&insert_columns=b` → `["a","b"]`), and nest a join arbitrarily deep the same
way (`join.room_types.join.rooms.relation=...`). A joined relation's own `where` can reach back
into its parent by the parent's `alias`: `join.room_types.where=eq(p.floor_count,3)` if the
root query set `alias=p`.

`select`, `order_by`, and `distinct_on` are each a comma-separated list, split by the same
quote-and-paren-aware scanner a function call's argument list uses — a literal inside a nested
call can itself contain a comma (`select=matched:in(status,'a,b')`) without breaking the split.

### `select`

A plain comma-list of `[alias:]expr` entries is the common case — a bare entry becomes its own
key (`select=id,name` → `{"id":"id","name":"name"}`); prefix one with `alias:` to select a
computed value under a chosen name (`select=total:agg(sum,payments.amount)`). The one
alternative is a single `own`/`full`-family call taking up the whole value — the two forms
never mix in one `select=`:

| `select=` | Compiles to |
|---|---|
| `own` / `full` | `["own"]` / `["full"]` |
| `own_except(a,b)` / `full_except(a,b)` | all columns except `a`, `b` |
| `own_and(total:agg(sum,payments.amount))` | all columns plus a computed `total` |
| `own_except_and(a,b; total:agg(...))` | except-list, then `;`, then the and-map |

### `order_by`

Each entry is an expression, optionally prefixed with a leading `-` for descending:
`order_by=-star_rating,name` is "star rating descending, then name ascending" — both
nulls-last, matching `POST /rel`'s own default. There's no query-string spelling of
`asc-nulls-first`/`desc-nulls-last`; use `POST /rel` for that.

## The filter expression grammar

`where=`, a `select=` computed entry, and `order_by=` all share one small expression language:

```
where=and(gte(year,1999),like(name,'%needle%'))
```

A bare word is a column/alias reference (`year`); a quoted string (`'needle'`, doubled quotes
to escape one — `'it''s open'`), a bare number, or `true`/`false`/`null` is a literal. Every
operator is written as a call, `name(arg, arg, ...)`, using its **word form**, never its
symbol — `gte`, not `>=`; see the [Operators reference](operators.md) for the full symbol ↔
word-form table (every table there lists the word form this grammar uses). `in`/`not_in`/
`any`/`all`/`between`/`not_between`/`agg`/`call` all work the same way, as calls with several
arguments: `in(status,'confirmed','checked_in')`, `between(100,base_price,500)`.

Not expressible in a query string at all — reach for [`POST /rel`](writing.md) instead: a bare
inline-object expression outside of `select=`; `get`/`set`/`get-set`; `$param`; `arr`/`lst`
literals; `own`/`full` used as a sub-expression rather than the whole of `select=`; an `agg`
with a filter (its fourth, optional argument — every `GET` `agg(...)` argument after the
function name lands in its argument list instead).

## Well-known queries over `GET`

A [well-known query](well-known-queries.md#calling-one-over-get-rel) has its own, simpler
`GET` form — `wellknown=<name>&params.<name>=<value>` — and doesn't use this grammar at all,
since it has no query tree of its own to decode.
