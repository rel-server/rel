---
icon: material/compass
---

# The query language

A query is one JSON object, sent as `POST /rel`. Every page in this section covers one part of
that object — there's no separate "read" and "write" grammar; the same tree does both (see
[Writing data back](writing.md)).

Examples throughout use the hotel-booking schema from [Getting started](../getting-started.md):
`hotel.chains` → `hotel.properties` → `hotel.room_types` → `hotel.rooms`, and `hotel.guests` →
`hotel.bookings` → `hotel.payments`. [Database reference](database-reference.md) has the full
diagram if you want the whole shape at a glance before diving in. [Query shape
reference](reference.md) does the same for the query language itself — the whole thing in one
page — before the rest of this section walks through it by example.

## In this section

- **[Query shape reference](reference.md)** — every field and every `Expression` form, in one
  place, each linked back to the page that explains it.
- **[Database reference](database-reference.md)** — the hotel-booking schema every example on
  these pages queries against.
- **[Shaping a query](shaping.md)** — roots, schema resolution, aliases.
- **[Filtering with `where`](filtering.md)** — comparisons, boolean logic, `between`/`in`/`any`.
- **[Selecting fields](selecting.md)** — `own`/`full` and their variants, object literals,
  `get`/`set`/`get-set`.
- **[Joining and embedding relations](joining.md)** — `join`/`on`, incoming vs. outgoing.
- **[Calling functions](functions.md)** — function-rooted and function-embedded nodes.
- **[Computed fields](computed-fields.md)** — `call`, the Postgres functional-column
  convention.
- **[Aggregates](aggregates.md)** — `agg`/`aggregate`.
- **[Operators reference](operators.md)** — every operator, JSON/array access, casts.
- **[Ordering, distinctness, and pagination](ordering-pagination.md)** — `order_by`,
  `distinct`/`distinct_on`, `limit`/`offset`.
- **[Writing data back](writing.md)** — write modes, `on_conflict`, writability.
- **[Batching queries in one request](batching.md)** — several queries, one transaction.
- **[Querying with `GET`](get-requests.md)** — the same read-only queries as a URL query string.
- **[Well-known queries](well-known-queries.md)** — named, pre-parsed, typed queries.
