---
icon: material/compass
---

# The query language

A query is one JSON object, sent as `POST /rel`. Every page in this section covers one part of
that object — there's no separate "read" and "write" grammar; the same tree does both (see
[Writing data back](writing.md)).

`POST /rel` also takes the same object as CBOR ([RFC 8949](https://www.rfc-editor.org/rfc/rfc8949)) when the
request's `Content-Type` is `application/cbor`. The query is the same tree, with CBOR maps, arrays, text strings,
numbers, booleans and null in the places JSON has them, and a write's `data` can also carry the types CBOR has
and JSON lacks ([value forms](writing.md#from-cbor)). Map keys must be text strings and appear once. A body that is not
valid CBOR is refused with `QUERY_MALFORMED_CBOR`. The format of the request does not decide the format of the response :
see [CBOR responses](#cbor-responses).

Examples throughout use the hotel-booking schema from [Getting started](../getting-started/index.md):
`hotel.chains` → `hotel.properties` → `hotel.room_types` → `hotel.rooms`, and `hotel.guests` →
`hotel.bookings` → `hotel.payments`. [Database reference](../example-database/reference.md) has the full
diagram if you want the whole shape at a glance before diving in. [Query shape
reference](reference.md) does the same for the query language itself — the whole thing in one
page — before the rest of this section walks through it by example.

## CBOR responses

A response is JSON unless the request's `Accept` header prefers `application/cbor` (`Accept: application/cbor`, or
`application/cbor, application/json;q=0.5`). `*/*`, `application/*` and no header at all are JSON. `GET /rel` and
`POST /rel` both negotiate, and the response says `Vary: Accept`. Errors are always JSON, whatever was asked.

The shape is the one the JSON response has : an array of rows (a sequence answers an array with one entry per
item), or the `count`/`stats`/`sql`/`query_plan`/`result` map of a [complex query](complex-query.md). What changes is
that values keep their column types instead of being squeezed into JSON's :

| Column type | CBOR |
|---|---|
| `smallint`, `integer`, `bigint`, `oid` | integer |
| `real`, `double precision` | float (a `real` stays a single) |
| `numeric` | tag 4 decimal fraction `[exponent, mantissa]`, keeping its scale (`1.50` is `[-2, 150]`) ; a bignum mantissa when it is large. `NaN` and the infinities are floats |
| `timestamptz` | tag 1 : integer seconds, or a float of seconds when there is a fraction |
| `timestamp` | tag 1, like `timestamptz` : the instant its wall clock stands for in the connection's time zone, what `ts::timestamptz` gives. The client cannot tell which zone that was, and a `timestamp` holding another zone's wall clock reads as the wrong instant. A wall clock skipped by a clock change gets the offset in force before it, one that occurs twice the offset in force after it. A time zone rel cannot load refuses the request |
| `date` | tag 100 : days since 1970-01-01 |
| `infinity`, `-infinity` (`date`, `timestamp`, `timestamptz`) | the text `"infinity"` / `"-infinity"` |
| `uuid` | tag 37 |
| `bytea` | byte string (JSON gives `\x...` text) |
| `boolean`, `text`, `varchar`, `char`, enums | boolean, text |
| `json`, `jsonb` | the same value as CBOR (numbers as in `numeric`) |
| arrays, composite types | arrays, maps |
| domains | their base type |
| anything else (`interval`, `inet`, ranges, geometry...) | what the JSON response gives for it (a `tsrange` keeps its wall-clock text, and so does a time inside a `jsonb`) |

Rows stream as they are read, like the JSON ones. If the query fails before the first row the answer is a JSON error ;
after it, the body just stops.

A [well-known query](well-known-queries.md) and a [route](../http/requests-responses.md#response-format) are negotiated
the same way.

## In this section

- **[Query shape reference](reference.md)** — every field and every `Expression` form, in one
  place, each linked back to the page that explains it.
- **[Database reference](../example-database/reference.md)** — the hotel-booking schema every example on
  these pages queries against.
- **[Shaping a query](shaping.md)** — roots, schema resolution, aliases.
- **[Filtering with `where`](filtering.md)** — comparisons, boolean logic, `between`/`in`/`any`.
- **[Selecting fields](selecting.md)** — `*`/`*~` and their variants, object literals,
  `get`/`set`/`col`, and `.` for a plain scope lookup.
- **[Joining and embedding relations](joining.md)** — `join`/`on`, incoming vs. outgoing.
- **[Calling functions](functions.md)** — function-rooted and function-embedded nodes.
- **[Computed fields](computed-fields.md)** — `call`, the Postgres functional-column
  convention.
- **[Aggregates](aggregates.md)** — `agg`/`aggregate`.
- **[Operators reference](operators.md)** — every operator, JSON/array access, casts.
- **[Ordering and pagination](ordering-pagination.md)** — `order_by`, `limit`/`offset`.
- **[Distinctness](distinctness.md)** — `distinct`/`distinct_on`, and pairing it with
  `order_by`.
- **[Writing data back](writing.md)** — write modes, `on_conflict`, writability.
- **[Batching queries in one request](batching.md)** — several queries, one transaction.
- **[Querying with `GET`](get-requests.md)** — the same read-only queries as a URL query string.
- **[Well-known queries](well-known-queries.md)** — named, pre-parsed, typed queries.
