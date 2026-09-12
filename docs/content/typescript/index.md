---
icon: material/language-typescript
---

# TypeScript client

`GET /rel/database.ts` serves a single, self-sufficient TypeScript file, generated fresh from
your live schema. Drop it into your project and import it — there's no build-time codegen step
to wire in, no ORM, and no `.save()`. It knows every table, column, function, and relationship
your database already has, and gives you typed query building over exactly the JSON shape
described in [The query language](../query-language/index.md).

## Fetching it

```sh
curl http://localhost:8080/rel/database.ts?schemas=hotel > src/database.ts
```

`schemas` restricts which schemas get included — omit it and every schema but `pg_catalog` is
exported. The endpoint is off by default outside of dev mode; see
[Configuration reference](../configuration/reference.md) for `http.typescript.enable` and `http.typescript.schemas`.
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
read. See [The query language ## Selecting fields](../query-language/selecting.md).

Only a column with no other way to end up with a value — not nullable, no default, not an
identity/generated column — is mandatory in that write type; everything else (a nullable
column, one with a default, an identity column) is optional, so inserting a new row only
requires typing out the columns that actually need it. This follows a column through a select
map's own rename too: `select: { display_name: "name" }` still requires `display_name` if
`name` itself is required.

`wellknown()` builds a call to a registered [well-known query](../query-language/well-known-queries.md) instead
of an ad hoc relation — same `Querier`, but its params and result shape come from the query's
own registered definition rather than from what you pass to `relation()`.

## Calling a function directly

`func()` builds a `Querier` you can still shape with `select`/`join`/`where`, the same as `relation()`. When you
just want to call a function and get its result back — no select, no join — use `call()` instead:

```ts
import { call } from "./database"

const properties = await call("hotel.rooms_available", { property_id: 1 })
```

`call()` takes the function's arguments — either the named object shown above or the equivalent positional
tuple (`call("hotel.rooms_available", [1])`) — checked against exactly what the function declares, and resolves
directly to a `Promise` of its return value: an array of rows for a set-returning function, a bare scalar for a
scalar one. There's no `Querier` to hold onto and no `.get()`/`.write()` step.

Arguments are checked field-for-field against the function's own signature, so a wrong argument name or type is
a compile error. A trailing argument with a database-side default is optional, the same way an optional column
is on write. If a function is overloaded (declared more than once with different argument lists), the arguments
you pass pick which overload's return type you get back.

`func()`'s own `arguments` field is checked the same way, with one difference: each argument may also be a
`["$param", name]` placeholder, since a `func()` query is built once and can be reused with different
`.get(params)`/`.write(params, data)` values, unlike `call()`, which runs immediately.

## database.json

`GET /rel/database.json` serves the same introspected schema as plain JSON instead of TypeScript
types — for a non-TypeScript consumer, or for tooling built directly on the raw introspected
facts. See [database.json](database-json.md).

## Keeping it in sync

`database.ts`/`database.json` are a snapshot of the schema at the moment you fetched them —
rel doesn't push updates to a client that already downloaded the file. If you add a column,
add a table, or change a relationship, re-fetch to pick it up; until you do, the old file just
describes the schema as it was, and a query against a field that no longer exists fails at the
server, not at compile time. If you're actively iterating on the schema, `typescript.helper_path`
(above) is the more convenient loop, since it rewrites the file on your behalf every time the
schema reloads — you still have to let your editor pick up the change, but there's no manual
re-fetch step.
