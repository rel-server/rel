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
a joined alias that isn't declared, and it's a compile error, not a runtime surprise. The same
goes for `relation()`/`func()`'s own first argument: `relation("hotel.bogus", ...)` is a compile
error too, with your editor autocompleting the relation/function names your schema actually has.

Hovering over `properties` (or any `.get()`/`.write()`/`call()` result) shows a plain, flattened
object literal — `{ id: number; name: string; rooms: {...}[] }` — at every nesting level, not the
chain of internal type names that computed it.

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

If you've already built a `relation()`/`func()` `Querier` for something you also want to embed
as a join elsewhere, you can pass it straight to `join()` instead of retyping its `select`/`proto`:

```ts
const roomWithFeatures = relation("hotel.rooms", {
  select: { id: "id", room_number: "room_number", features: "features" },
})

const properties = await relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms<;id:property_id", roomWithFeatures),
  },
})).get()
```

`join()` still works out `on`/`schema`/`relation` from the shortcut regardless — a root
`Querier` never carries those — it just reuses everything else from the `Querier` you pass in.

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

### Naming a query's shape

If you want to name a query's shape — say, as a function parameter's type elsewhere in your
code — `ShapeOf`/`WriteShapeOf` pull it straight off a `Querier`, instead of writing
`Awaited<ReturnType<typeof someQuery.get>>` by hand. Like `.get()`'s own return value, `ShapeOf`
is the array (a relation/function root is always an array of rows) — index it with `[number]`
for a single row's type:

```ts
import { relation, ShapeOf, WriteShapeOf } from "./database"

const properties = relation("hotel.properties", {
  select: { id: "id", name: "name" },
})

type Properties = ShapeOf<typeof properties> // { id: number; name: string }[]
type PropertiesWrite = WriteShapeOf<typeof properties>

function printProperty(p: Properties[number]) {
  console.log(p.name)
}
```

Both are flattened the same way `.get()`/`.write()` already are, so hovering `Properties` shows
the actual object shape, not a reference back to `typeof properties`.

## Attaching behavior to rows

`relation()`/`func()`/`join()` all accept a `proto` field alongside `select`/`join`/`where`: a map
of getters, setters, methods, and property descriptors, typed against exactly the row shape that
node produces. Once a node's rows come back, `proto` is turned into a real prototype object (once
per query node, reused across every row it returns) and every row is given that prototype — its
members appear on each row without being copied into the row's own JSON data:

```ts
const properties = await relation("hotel.properties", {
  proto: {
    get is_luxury(): boolean {
      return (this.star_rating ?? 0) >= 4
    },
    describe(): string {
      return `${this.name} (${this.star_rating ?? "unrated"})`
    },
  },
}).get()

properties[0].is_luxury // boolean, computed client-side, no extra column selected
properties[0].describe() // ordinary method, same `this`
```

Annotate the return type on every getter/method whenever `proto` mixes the two (a `get` alongside
a plain method, as above) — TypeScript's inference of `this` inside a mixed object literal like
this only works when every member's return type is explicit. A `proto` made up entirely of
getters, or entirely of plain methods, doesn't need the annotations, but mixing without them
silently degrades `this` to `any` throughout the whole object.

`this` inside `proto` is typed as the row's own computed shape — including any joined
relation's own `proto`, so a parent's `proto` can read a nested join's decorated members too:

```ts
const properties = await relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms<;id:property_id", {
      proto: {
        get label() {
          return `Room ${this.room_number}`
        },
      },
    }),
  },
  proto: {
    get room_count() {
      return this.rooms.length
    },
  },
})).get()

properties[0].rooms[0].label // uses the join's own `proto`
```

`proto` only adds behavior — it never changes what a row's write shape looks like, so writing
back with `.write()` is unaffected by whether the query you read it with declared a `proto`.

A `proto` entry can also be a property descriptor (`{get, set, enumerable}`), not just a plain
getter/method written inline — this is what lets a reusable helper compose accessors into
`proto` via object spread. `database.ts` ships one such helper per [typed wire value](#typed-wire-values)
worth converting client-side; see that section for the full list. Spreading one in adds its
member alongside anything else in `proto`:

```ts
const properties = await relation("hotel.properties", {
  proto: {
    ...timestampTzAccessor("created_at"),
    describe(): string {
      return `${this.name} (${this.id})`
    },
  },
}).get()

properties[0].created_at            // Timestamptz — the raw column, untouched
properties[0].created_at_as_date    // Temporal.Instant — derived, not stored
properties[0].created_at_as_date = Temporal.Now.instant() // writable, if the helper defines a setter
```

A helper's own suffix (`_as_date`, `_as_bigint`, `_as_range`, ...) says what it converts to, and
whether the resulting member is writable depends on whether the helper defines a setter — a
range accessor, for instance, is read-only.

## Typed wire values

Every column comes back typed as the JS type its wire value actually has, not the type its
Postgres type name might suggest. A `timestamptz` column doesn't come back as `number` or `Date`;
it comes back as a distinct branded string type (`Timestamptz`), because the value on the wire is
a string, and giving it a `Date`-shaped type would let you call `.getFullYear()` on something that
has no such method at runtime. This applies to every Postgres type this section covers.

The branded types (defined once in `database.ts` and referenced by every column of that type):

| Postgres type | TS type | Notes |
|---|---|---|
| `date`, `time`, `timetz`, `timestamp`, `timestamptz` | `PgDate`, `PgTime`, `PgTimetz`, `Timestamp`, `Timestamptz` | see below for accessors |
| `interval` | `Interval` | see below |
| `money` | `Money` | wire value is a locale-formatted, quoted string (`"$10.50"`) |
| `int8`, `numeric` | `Int8`, `PgNumeric` | see below — precision loss otherwise |
| `int4range`, `int8range`, `numrange`, `daterange`, `tsrange`, `tstzrange` | `Int4Range`, `Int8Range`, `NumRange`, `DateRange`, `TsRange`, `TstzRange` | see below |
| the same six, as multiranges | `Int4MultiRange`, `Int8MultiRange`, `NumMultiRange`, `DateMultiRange`, `TsMultiRange`, `TstzMultiRange` | |
| `inet`, `cidr`, `macaddr`, `macaddr8`, `bit`, `varbit`, `point`, `line`, `lseg`, `box`, `path`, `polygon`, `circle`, `tsvector`, `tsquery`, `xml`, `pg_lsn` | `Inet`, `Cidr`, `MacAddr`, `MacAddr8`, `PgBit`, `PgVarbit`, `PgPoint`, `PgLine`, `PgLseg`, `PgBox`, `PgPath`, `PgPolygon`, `PgCircle`, `TsVector`, `TsQuery`, `Xml`, `PgLsn` | branded strings, no richer conversion (except `point`, below) |

A branded type has no runtime representation — the value is a plain string, and the brand only
makes one Postgres type's string distinguishable from another's at compile time. Passing a
`Timestamp`-branded string where a `Timestamptz` is expected, for instance, is a compile error,
even though both are strings at runtime.

An array of any branded type (`Timestamptz[]`, `Int4Range[]`, ...) needs no special handling — the
wire format is a plain JSON array of the same per-element string format.

### Reading a richer value

A branded type's own accessor helper (imported from `database.ts`, spread into `proto`) converts
it into a genuinely richer client-side value:

```ts
import { relation } from "./database"
import { timestampTzAccessor, int8Accessor, tstzRangeAccessor, pointAccessor } from "./database"

const properties = await relation("hotel.properties", {
  proto: {
    ...timestampTzAccessor("created_at"), // created_at_as_date : Temporal.Instant
  },
}).get()
```

| Source type | Accessor | Result |
|---|---|---|
| `Timestamptz` | `timestampTzAccessor` | `Temporal.Instant` |
| `Timestamp` | `timestampAccessor` | `Temporal.PlainDateTime` (naive — no time zone assumed) |
| `PgDate` | `dateAccessor` | `Temporal.PlainDate` |
| `Interval` | `intervalAccessor` | `Temporal.Duration` |
| `Int8` | `int8Accessor` | `bigint`, via `BigInt()` — exact |
| `Int4Range`/`Int8Range`/`NumRange`/`DateRange`/`TsRange`/`TstzRange` | `int4RangeAccessor`/... | `Range<T>` (below), `T` matching the table above |
| `PgPoint` | `pointAccessor` | `{x: number, y: number}` |

`PgTimetz` and `numeric` have no accessor: `Temporal` has no "time of day with a UTC offset, no
date" class for the former, and no native JS type represents an arbitrary-scale decimal exactly
for the latter — a `Number()`-based conversion would just reintroduce the precision loss
`PgNumeric` exists to avoid. Comparing two `PgNumeric` values needs `compareNumeric` (also exported
from `database.ts`), not `<`/`>`, for the same reason.

Every temporal accessor returns a [`Temporal`](https://tc39.es/proposal-temporal/docs/) value, not
a `Date` — `Temporal`'s classes are immutable and genuinely distinct from one another (a
`Temporal.PlainDateTime` cannot satisfy a `Temporal.Instant`-typed parameter, cast or no cast),
which `Date` alone can't offer. `database.ts` references `Temporal` as a global, the same way it
already references `Date`/`Promise`/`fetch` — until `Temporal` ships natively everywhere, your own
project is responsible for installing a polyfill (`@js-temporal/polyfill`) if your target runtime
doesn't have it yet.

### Writing one back

Each branded type with a meaningful validity check has a matching `as<Type>` cast (`asTimestamptz`,
`asInt8`, `asInterval`, ...), also exported from `database.ts`:

```ts
import { asTimestamptz } from "./database"

await relation("hotel.properties", { select: { id: "id", created_at: "created_at" } }).write({
  id: 1,
  created_at: asTimestamptz(new Date()), // or a Temporal.Instant, or a pre-formatted string
})
```

`asTimestamptz` throws if given a string with no time zone offset — Postgres would otherwise
silently reinterpret it using the session's own time zone. `asMoney` and every range cast only
brand the value, since nothing about their string shape is unsafe to skip validating. `asInt8`
accepts `bigint | number | string`, converting via `String()` — Postgres's own `int8` input parser
rejects a malformed string on write, so there's no client-side re-check.

### Ranges

`Range<T>`, one immutable class shared by every range type (`Range<number>` for `Int4Range`,
`Range<bigint>` for `Int8Range`, `Range<Temporal.Instant>` for `TstzRange`, and so on):

```ts
range.lower           // T | null — null means unbounded
range.upper           // T | null
range.lowerInclusive  // boolean
range.upperInclusive  // boolean
range.isEmpty         // boolean
range.contains(value) // boolean
range.overlaps(other) // boolean
range.equals(other)   // boolean
```

`MultiRange<T>` wraps zero or more `Range<T>` segments (`multirange.ranges`) and supports
`contains(value)` the same way, checking every segment.

### Intervals

`intervalAccessor` returns a [`Temporal.Duration`](https://tc39.es/proposal-temporal/docs/duration.html),
the natural counterpart for a span rather than a point in time. It assumes the session's
`IntervalStyle` is `postgres` (Postgres's own default) and throws on a string in a different style.
`asInterval` writes a `Temporal.Duration` or a string back — a `Temporal.Duration` is serialized
via its own ISO 8601 `.toString()` (e.g. `"P1DT2H3M4S"`), which Postgres's interval parser accepts
regardless of the session's output-side `IntervalStyle`.

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
