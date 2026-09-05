---
icon: material/rocket
---

# Getting started

This walks through running rel against a real schema, then making a read and a write request.
The schema is a small hotel-booking database — chains, properties, rooms, guests, bookings —
close enough to a real app to be worth keeping around as you read the rest of the docs.

## Run rel against a database

Point rel at Postgres with a single connection string:

```sh
REL_PG__URI="postgres://user:pass@localhost:5432/mydb" rel
```

That's the entire minimum configuration. On startup, rel connects, introspects every table,
view, function, and constraint reachable on the connecting role's search path, and starts
serving on port `8080`.

Every setting has an environment-variable form (`REL_<SECTION>__<KEY>`, `__` separating
nesting), a config-file form (TOML, YAML, or HUML), and a `--flag` form — flags win over
environment variables, which win over the config file. `pg.uri` above is `REL_PG__URI`,
`--pg.uri`, or `pg.uri` under `[pg]` in a config file, interchangeably.

## A query, end to end

Every query goes to `POST /rel`, as JSON, whether it reads or writes. This one reads
four-and-up-star properties, with each property's room types and the chain it belongs to:

```json
POST /rel

{
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
}
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

See [The query language](query-language/index.md) for `where`, `select`, and `join` in full. A
read-only query like this one is also reachable as a plain `GET` with the same shape encoded
into the URL — see [Querying with GET](query-language/get-requests.md) — handy for anything you
want cacheable, bookmarkable, or usable from a plain link.

## The same shape, written back

Take that result — or any object shaped like it — edit it, and send it to the same endpoint,
wrapped in `query`/`data`:

```json
POST /rel

{
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
  "data": {
    "id": 1,
    "name": "Marina Bay Grand Hotel",
    "room_types": [
      { "id": 1, "property_id": 1, "name": "Deluxe", "base_price": "199.00", "capacity": 2 },
      { "property_id": 1, "name": "Penthouse", "base_price": "899.00", "capacity": 2 }
    ]
  }
}
```

This updates the property's `name`, updates the existing `Deluxe` room type's price, and
inserts the new `Penthouse` room type — its `id` isn't in the payload because it doesn't exist
yet, and rel fills it in and threads it into the new row's own `property_id` itself. Any
existing room type left out of the array would be deleted, because `room_types` is joined as
an incoming relation, and an incoming relation's default write mode is `merge`: insert what's
new, update what matches, delete what's missing. Change that per relation with `write_mode` —
see [The query language ## Writing data back](query-language/writing.md).

## Try it against the dev fixture

The repo ships this exact schema as a throwaway dev database:

```sh
just db-fresh   # tear down, bring up Postgres, migrate, seed — leaves it running
just run        # starts rel against it, on :8080
```

`just db-psql` opens a `psql` shell into the same database if you want to look at the schema
or the seeded rows directly.

## Where to go next

- [Configuration](configuration/index.md) — every setting beyond `pg.uri`: secrets, logging, pool
  sizing, well-known-query directories.
- [The query language](query-language/index.md) — the full shape of `where`/`select`/`join`,
  operators, aggregates, and every write mode.
- [HTTP routes](http-routes.md) — arbitrary server-side logic as a Postgres function: login
  flows, server-rendered HTML via Jet templates, file uploads, static files.
- [Authentication](configuration/authentication.md) — username/password, OpenID Connect, and SAML, and how a
  request ends up running as a particular Postgres role.
- [Docker deployment](configuration/docker-deployment.md) — running the published image behind
  `jwilder/nginx-proxy`, with automatic TLS.
