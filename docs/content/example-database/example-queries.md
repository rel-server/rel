---
icon: material/text-search
---

# Example queries

A tour of the query language against the hotel schema — one query per capability, each one
`curl`-able directly against a running rel pointed at the seeded fixture (see [Launch it
yourself](launch-it-yourself.md)). Most are `POST /rel`; a couple show the same grammar as a
plain `GET`, per [Querying with GET](../query-language/get-requests.md).

## Filtering: boolean logic and pattern matching

Four-and-up-star properties with "Grand" in the name:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "properties",
    "schema": "hotel",
    "where": ["and", [">=", "star_rating", 4], ["like", "name", ["%Grand%"]]],
    "select": { "id": "id", "name": "name", "star_rating": "star_rating" }
  }'
```

A bare string in an expression is always a column/alias reference; `["%Grand%"]` — a
one-element array — is how a literal string is spelled instead. See [Filtering with
`where`](../query-language/filtering.md).

## The same kind of filter, as a plain `GET`

Room types priced between 100 and 300, cheapest first:

```sh
curl 'http://localhost:8080/rel?relation=room_types&schema=hotel&where=between(100,base_price,300)&select=id,property_id,name,base_price&order_by=base_price&limit=5'
```

`where=`'s operator-as-call grammar (`between(...)`, `in(...)`, `gte(...)`) is a second spelling
of the same expressions the JSON body uses — see [Querying with
GET](../query-language/get-requests.md).

## Joining both directions in one query

A confirmed booking, with the guest it belongs to (outgoing: `bookings.guest_id → guests.id`)
and the room it's for (outgoing: `bookings.room_id → rooms.id`):

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "bookings",
    "schema": "hotel",
    "where": ["=", "status", ["confirmed"]],
    "limit": 2,
    "join": {
      "guest": { "relation": "guests", "schema": "hotel", "on": { "id": "guest_id" }, "select": { "id": "id", "email": "email" } },
      "room": { "relation": "rooms", "schema": "hotel", "on": { "id": "room_id" }, "select": { "id": "id", "room_number": "room_number" } }
    },
    "select": { "id": "id", "stay": "stay", "guest": "guest", "room": "room" }
  }'
```

Both `guest` and `room` come back as single objects, never arrays — `bookings` holds both
foreign keys, so both joins are outgoing. See [Joining and embedding
relations](../query-language/joining.md).

## A self-join: staff and their manager

`hotel.staff.manager_id` points back at another row in the same table — `hotel.staff`'s own
instance of the self-join case:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "staff",
    "schema": "hotel",
    "alias": "s",
    "limit": 5,
    "join": {
      "manager": { "relation": "staff", "schema": "hotel", "on": { "id": "manager_id" }, "select": { "id": "id", "name": "name" } }
    },
    "select": { "id": "id", "name": "name", "role": "role", "manager": "manager" }
  }'
```

The General Manager at the top of each property's chain comes back with `"manager": null` —
`manager_id` is nullable. See [Shaping a query ## Aliases and
self-joins](../query-language/shaping.md#aliases-and-self-joins).

## Aggregating a joined relation

Each property's room type count, and how many of those are under 150 a night:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "properties",
    "schema": "hotel",
    "limit": 5,
    "join": {
      "room_types": { "relation": "room_types", "schema": "hotel", "on": { "property_id": "id" } }
    },
    "select": {
      "name": "name",
      "room_type_count": ["agg", "count", [[".", "room_types", "id"]]],
      "budget_type_count": ["agg", "count", [[".", "room_types", "id"]], ["<", [".", "room_types", "base_price"], 150]]
    }
  }'
```

`agg` only works over a joined **incoming** relation — `room_types.property_id` points at
`properties.id`, so `room_types` qualifies. `budget_type_count`'s fourth argument filters which
rows get counted, independent of the query's own (absent) `where`. See
[Aggregates](../query-language/aggregates.md).

## Computed fields

`booking_nights` and `booking_total_paid` are ordinary Postgres functions taking a `bookings`
row, selectable by bare name exactly like a real column:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "bookings",
    "schema": "hotel",
    "limit": 3,
    "select": { "id": "id", "nights": "booking_nights", "total_paid": "booking_total_paid" }
  }'
```

See [Computed fields](../query-language/computed-fields.md) for every computed field this
schema defines and the eligibility rules behind them.

## A function-rooted query: full-text search

`hotel.search_properties(query)` is a `SETOF hotel.properties` function, queried as its own
relation rather than a computed column — it searches `properties.description_search`, a stored
`tsvector` generated from `description`:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "function": "search_properties",
    "schema": "hotel",
    "arguments": { "query": ["beach"] },
    "select": { "id": "id", "name": "name" }
  }'
```

## A function-rooted query with a default argument

`hotel.rooms_available(property_id, on_date default current_date)` lists a property's rooms
with no overlapping booking on a given date — `on_date` is optional:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "function": "rooms_available",
    "schema": "hotel",
    "arguments": { "property_id": 1, "on_date": ["2026-06-01"] },
    "select": ["own"]
  }'
```

See [Calling functions](../query-language/functions.md) — `arguments` can be positional (an
array) or named (an object, as above), never both.

## `distinct_on`, as a plain `GET`

The cheapest room type per property — one row per `property_id`, whichever sorts first under
`order_by`:

```sh
curl 'http://localhost:8080/rel?relation=room_types&schema=hotel&distinct_on=property_id&order_by=property_id,base_price&select=property_id,name,base_price&limit=5'
```

`distinct_on`'s expressions must be a leading prefix of `order_by` — see
[Distinctness](../query-language/distinctness.md).

## Filtering an array column

Rooms with a "Sea View" among their `features` (a plain `text[]`, not a joined relation):

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "rooms",
    "schema": "hotel",
    "where": ["@>", "features", ["arr", ["Sea View"]]],
    "limit": 3,
    "select": { "id": "id", "room_number": "room_number", "features": "features" }
  }'
```

## Reaching into a composite-typed column

`guests.billing_address` is a composite column (`hotel.address`), not a join — `.` reaches into
one of its fields the same way it flattens a joined relation's column:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "guests",
    "schema": "hotel",
    "where": ["is_not_null", "billing_address"],
    "limit": 3,
    "select": { "id": "id", "email": "email", "city": [".", "billing_address", "city"] }
  }'
```
