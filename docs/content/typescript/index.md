---
icon: material/language-typescript
---

# TypeScript client

`GET /rel/database.ts` serves a single, self-sufficient TypeScript file, generated fresh from
your live schema. Drop it into your project and import it — there's no build-time codegen step
to wire in, no ORM, and no `.save()`. It knows every table, column, function, and relationship
your database already has, and gives you typed query building over exactly the JSON shape
described in [The query language](query-language/index.md).

## Fetching it

```sh
curl http://localhost:8080/rel/database.ts?schemas=hotel > src/database.ts
```

`schemas` restricts which schemas get included — omit it and every schema but `pg_catalog` is
exported. The endpoint is off by default outside of dev mode; see
[Configuration reference](configuration/reference.md) for `http.typescript.enable` and `http.typescript.schemas`.
If you'd rather have your editor/LSP watch a real file on disk instead of re-curling by hand,
`typescript.helper_path` has rel write `database.ts` straight to a path of your choosing, on
startup and on every schema reload.

The file has no `import`s and depends on nothing else in your project — copy it in, commit it
if you like, and it just works.

## Building a query

`database.ts` exports `relation()`, `func()`, and `wellknown()` — thin builders that return a
`Querier`, typed against your actual schema:

```ts
import { relation } from "./database"

const properties = await relation("hotel.properties", (join) => ({
  where: [">=", "star_rating", 4],
  join: {
    rooms: join("hotel.rooms<;id:property_id", {
      select: "*",
    }),
  },
  select: { id: "id", name: "name", star_rating: "star_rating", rooms: "rooms" },
})).get()
```

`properties` comes back typed as an array of exactly the shape you asked for — `id: number`,
`name: string`, `rooms: Table__Hotel__Rooms[]` — not `any`. Get a column name wrong, or select
a joined alias that isn't declared, and it's a compile error, not a runtime surprise.

`relation()`'s second argument is a callback, and it's what makes a joined column's shortcut
type-check correctly: the `join` it receives already knows which relation it's being called
from, so it only offers the foreign keys reachable from *that* relation, and only accepts a
shortcut that's actually valid there — get it wrong and it's a compile error, with your editor's
autocomplete listing the valid shortcuts for that relation. The scoping recurses: a join nested
inside another join gets a `join` of its own, scoped to *its* target, so the same type-checking
applies at every depth without you having to tell it what relation you're embedding into:

```ts
const properties = await relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms<;id:property_id", (join) => ({
      join: {
        room_type: join("hotel.room_types>;id:room_type_id"),
      },
    })),
  },
})).get()
```

The second argument is also optional entirely: `relation("hotel.properties")` alone, with no
query, is a bare select-all (own columns plus every joined relation, the same default an omitted
`select` already has server-side).

Writing back uses the same `Querier`, with `.write()` instead of `.get()`:

```ts
await relation("hotel.properties", {
  select: { id: "id", name: "name" },
}).write({ id: 1, name: "Marina Bay Grand Hotel" })
```

`write()`'s argument type is derived independently from `.get()`'s return type, because they
can genuinely differ — a column marked `get` in your `select` is read-only and drops out of the
write type entirely; one marked `set` is the opposite, accepted on write but never returned on
read. See [The query language ## Selecting fields](query-language/selecting.md).

`wellknown()` builds a call to a registered [well-known query](query-language/well-known-queries.md) instead
of an ad hoc relation — same `Querier`, but its params and result shape come from the query's
own registered definition rather than from what you pass to `relation()`.

## database.json

`GET /rel/database.json` is the same introspected structure — types, relations, columns,
constraints, indexes, relationships, functions, computed properties, well-known queries — as
plain JSON instead of TypeScript types, gated and filtered by the same `schemas` query param and
`http.typescript.*` configuration as `database.ts`. Reach for it when you're not in a TypeScript
project at all, when you want to feed the schema into your own tooling (a generator, a schema
browser, a non-JS client), or when you're building something like an automatic admin UI that
needs the raw introspected facts — constraint names, index coverage, nullability, every declared
foreign key including one the query engine can't efficiently join — rather than `database.ts`'s
already-collapsed TypeScript types.

Unlike `database.ts`, which only lists a foreign key that's actually usable in a `join()`,
`database.json`'s own `relationships` section lists every declared foreign key touching an
exported relation, in both directions, with an `eligible` flag telling you which ones are
join-safe — a relation-picker UI built off this export can still show every real relationship,
even one it has to resolve with its own lookup instead of a `join()`.

The top-level response has these sections, every object key keyed `"schema.name"` unless noted
otherwise and repeating its own `schema`/`name` fields :

- **`types`** — every scalar/enum/domain/array/composite type referenced by a column or function
  argument/return type, transitively. A composite entry points at its backing relation by key ;
  that relation is always present in `relations`, even from a schema `schemas` didn't whitelist.
- **`relations`** — one entry per table/view/materialized view/standalone composite type (`kind`
  says which), with its columns (type, nullability, identity/generated/updatable flags, default
  presence), its primary key and unique constraints (both named), and its indexes.
- **`relationships`** — every declared foreign key touching a relation, both directions, each with
  the target relation, the join columns (`on`, in the same shape the query language's own join
  `on` field takes), cardinality (`unique`), the backing constraint's name, and `eligible` (false
  when the FK-holding side isn't indexed — still a real relationship, just not `join()`-safe).
- **`functions`** / **`functions_by_name`** — every plain function, with every argument's mode
  (`in`/`out`/`inout`/`variadic`/`table` — not just the callable subset `database.ts` renders),
  its volatility/strictness, and its return shape (a `types` key, or `null` when the row shape is
  built from its own `out`/`table` arguments instead).
- **`computed_properties`** — the same per-relation computed-field discovery `database.ts`'s
  `ComputedProperties` documents.
- **`wellknowns`** — each well-known query's raw compiled query JSON and declared parameters.

See `specs/database-json.md` for the exact shape of every field.

## Keeping it in sync

`database.ts`/`database.json` are a snapshot of the schema at the moment you fetched them —
rel doesn't push updates to a client that already downloaded the file. If you add a column,
add a table, or change a relationship, re-fetch to pick it up; until you do, the old file just
describes the schema as it was, and a query against a field that no longer exists fails at the
server, not at compile time. If you're actively iterating on the schema, `typescript.helper_path`
(above) is the more convenient loop, since it rewrites the file on your behalf every time the
schema reloads — you still have to let your editor pick up the change, but there's no manual
re-fetch step.
