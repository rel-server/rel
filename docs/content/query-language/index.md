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
see [CBOR responses](#cbor-responses). A body may also use [structures](#structures), which `cbor-x` writes by default.

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

### Structures

Rows of a query have the same field names from the first to the last. `Accept: application/cbor; structures=1` asks for
each list of field names to be written once, in the first row that has it, and for the other rows to point at it : the
response is about half the size for typical rows, and a client that understands them reads it about three times
faster. The answer says `Content-Type: application/cbor; structures=1`. Without the parameter the rows are plain maps.

The tags are the record extension of [`cbor-x`](https://github.com/kriszyp/cbor-x#record--object-structures), not a
registered CBOR extension, so only a client that knows it can read them ; the TypeScript client asks for them by default.
Only the rows of a query (and the rows of what it embeds) use them : `json` and `jsonb` values, `stats`, `sql` and
`query_plan` stay plain maps, and so do route responses, which answer plain CBOR either way. When the header lists
`application/cbor` with and without the parameter, the one with the higher `q` wins and the parameter wins a tie.

A request body can use them whatever `Accept` says, with no parameter : the tags are read wherever a map is accepted.
Definitions are read in order, a reference needs its definition before it, and an id defined again replaces the earlier
one from there on. A definition whose names are not distinct text strings, a reference to an id never defined, and a
reference whose number of values differs from its definition's are refused. The two other record forms `cbor-x` can
read (tag `0xdffe` and tag 105) are not read.

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
