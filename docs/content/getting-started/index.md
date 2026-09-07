---
icon: material/rocket
---

# Getting started

This walks through running rel against a real schema, then making a read and a write request,
with plain `curl` — no client library needed to try any of this out. The schema is a small
hotel-booking database — chains, properties, rooms, guests, bookings — close enough to a real
app to be worth keeping around as you read the rest of the docs.

## Run rel against a database

Point rel at Postgres with a single connection string:

```sh
REL_PG__URI="postgres://user:pass@localhost:5432/mydb" rel
```

That's the entire minimum configuration. On startup, rel connects, introspects every table,
view, function, and constraint reachable on the connecting role's search path, and starts
serving on port `8080`. See [Run it manually](run-it-manually.md) if you don't have a `rel`
binary yet — a prebuilt release, Docker, or building from source.

Every setting has an environment-variable form (`REL_<SECTION>__<KEY>`, `__` separating
nesting), a config-file form (TOML, YAML, or HUML), and a `--flag` form — flags win over
environment variables, which win over the config file. `pg.uri` above is `REL_PG__URI`,
`--pg.uri`, or `pg.uri` under `[pg]` in a config file, interchangeably.

## Before the first request: anonymous access

None of the `curl` examples below send a session — they're all unauthenticated requests. rel
only serves those if `pg.query.anonymous_role` (default `~anonymous`) is a real role in your
database; otherwise anonymous access is disabled outright and every request without a session
gets a `401`:

```sql
create role "~anonymous";
grant usage on schema hotel to "~anonymous";
grant select, insert, update, delete on all tables in schema hotel to "~anonymous";
grant execute on all functions in schema hotel to "~anonymous";
```

Skip this if you're following [Launch it yourself](../example-database/launch-it-yourself.md) —
the dev fixture's schema already grants this to itself. See
[Authentication](../http/authentication.md) for running real, per-user sessions instead of
anonymous access once you're past this page.

## A query, end to end

Every query goes to `POST /rel`, as JSON, whether it reads or writes. This one reads
four-and-up-star properties, with each property's room types and the chain it belongs to:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "relation": "properties",
    "schema": "hotel",
    "where": [">=", "star_rating", 4],
    "join": {
      "room_types": {
        "relation": "room_types",
        "schema": "hotel",
        "on": { "property_id": "id" }
      },
      "chain": {
        "relation": "chains",
        "schema": "hotel",
        "on": { "id": "chain_id" }
      }
    },
    "select": {
      "id": "id",
      "name": "name",
      "star_rating": "star_rating",
      "chain": "chain",
      "room_types": "room_types"
    }
  }'
```

`room_types` is joined the way a child row usually is — its own `property_id` points back at
the parent's `id` — and comes back as an array, one entry per room type. `chain` is the
opposite direction: `properties.chain_id` points *at* `chains.id`, so it comes back as a
single object, not an array. rel tells the two apart from the join's `on` shape; you never
declare "to-one" or "to-many" yourself.

```json
[
  {
    "id": 1,
    "name": "Marina Bay Hotel",
    "star_rating": 4,
    "chain": { "id": 3, "name": "Blue Horizon" },
    "room_types": [
      { "id": 1, "property_id": 1, "name": "Deluxe", "base_price": "189.00", "capacity": 2 },
      { "id": 2, "property_id": 1, "name": "Suite", "base_price": "349.00", "capacity": 4 }
    ]
  }
]
```

See [The query language](../query-language/index.md) for `where`, `select`, and `join` in full.

## The same query, as a plain `GET`

A read-only query like the one above is also reachable as a plain `GET`, the whole tree encoded
into the URL instead of a JSON body — handy for anything you want cacheable, bookmarkable, or
usable from a plain link (or, here, a `curl` one-liner with no `-d` at all):

```sh
curl 'http://localhost:8080/rel?relation=properties&schema=hotel&where=gte(star_rating,4)&select=id,name,star_rating,chain,room_types&join.chain.relation=chains&join.chain.schema=hotel&join.chain.on.id=chain_id&join.room_types.relation=room_types&join.room_types.schema=hotel&join.room_types.on.property_id=id'
```

This decodes to exactly the same query tree as the `POST` body above, and returns the same rows.
`GET /rel` only ever reads — no `data`, no batching, no write-only fields — see [Querying with
GET](../query-language/get-requests.md) for the full grammar (`where=`'s operator-as-call
syntax, `select=`'s `own`/`full` shorthands, and more).

## Writing, nested, before the parent even has an id

Send `{"query": <Relation>, "data": <payload>}` instead of a bare relation to write. This
creates a brand-new property and two new room types in one request — `data` is an array (a
write's payload always mirrors what reading this same query would produce, and a relation reads
back as an array of rows, root included) and neither the property nor its room types carry an
`id`, because none of them exist yet:

```sh
curl http://localhost:8080/rel \
  -H 'Content-Type: application/json' \
  -d '{
    "query": {
      "relation": "properties",
      "schema": "hotel",
      "join": {
        "room_types": {
          "relation": "room_types",
          "schema": "hotel",
          "on": { "property_id": "id" }
        }
      },
      "select": { "id": "id", "name": "name", "room_types": "room_types" }
    },
    "data": [{
      "name": "Marina Bay Grand Hotel",
      "room_types": [
        { "name": "Deluxe", "base_price": "199.00", "capacity": 2 },
        { "name": "Penthouse", "base_price": "899.00", "capacity": 2 }
      ]
    }]
  }'
```

```json
[
  {
    "id": 14,
    "name": "Marina Bay Grand Hotel",
    "room_types": [
      { "id": 43, "property_id": 14, "name": "Deluxe", "base_price": "199.00", "capacity": 2 },
      { "id": 44, "property_id": 14, "name": "Penthouse", "base_price": "899.00", "capacity": 2 }
    ]
  }
]
```

rel resolves the write order itself: `properties` is inserted first, and its freshly generated
`id` is threaded into each `room_types` row as it's inserted right after, all inside one
transaction — no client-side orchestration, no separate request to get an id back before you
can insert what depends on it.

Send that same shape again, this time with `id`s already filled in from a prior read, and rel
updates matching rows instead of inserting duplicates — an incoming relation like `room_types`
here defaults to `merge`: insert what's new, update what matches, delete what the payload leaves
out entirely. The root relation defaults to `insert` instead (as above); set `write_mode` on it
explicitly (`"write_mode": "upsert"`) to update an existing row rather than insert a new one. See
[The query language ## Writing data back](../query-language/writing.md) for every write mode and
how deletes are ordered against foreign keys.

## Where to go next

- [Launch it yourself](../example-database/launch-it-yourself.md) — bring up this exact schema,
  seeded with fake data, and point rel at it locally.
- [Run it manually](run-it-manually.md) — a prebuilt release binary, the published Docker image,
  or building from source.
- [Docker deployment](docker-deployment.md) — a realistic deployment: rel and Postgres in
  Docker, fronted by a reverse proxy, with automatic TLS.
- [Configuration](../configuration/index.md) — how settings are supplied and layered; the
  [Configuration reference](../configuration/reference.md) lists every setting beyond `pg.uri`:
  secrets, logging, pool sizing, well-known-query directories.
- [The query language](../query-language/index.md) — the full shape of `where`/`select`/`join`,
  operators, aggregates, and every write mode.
- [HTTP layer](../http/index.md) — arbitrary server-side logic as a Postgres function: login
  flows, server-rendered HTML via Jet templates, file uploads, static files.
- [Authentication](../http/authentication.md) — username/password, OpenID Connect, and SAML, and
  how a request ends up running as a particular Postgres role.
