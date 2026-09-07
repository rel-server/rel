# database.json

`GET /rel/database.json` serves the same introspected schema [`database.ts`](index.md) renders
as TypeScript, as plain JSON instead. Reach for it when you're not in a TypeScript project at
all, when you want to feed the schema into your own tooling (a generator, a schema browser, a
non-JS client), or when you're building something like an automatic admin UI that needs the raw
introspected facts — constraint names, index coverage, nullability, every declared foreign key
including one the query engine can't efficiently join — rather than `database.ts`'s
already-collapsed TypeScript types.

It's wired up exactly like `database.ts`: gated behind `http.typescript.enable`, GET only, and
filtered by the same `schemas` query param (intersected with the configured
`http.typescript.schemas` whitelist — a `schemas` value outside the whitelist is a 400, not a
silent drop) and `http.typescript.blacklist`. See
[Configuration reference](../configuration/reference.md) for both settings. The response is
`Content-Type: application/json; charset=utf-8`, and every object key is `snake_case`.

The `schemas` whitelist and the blacklist both decide which relations/functions are exported as
first-class, independently-reachable entries (their own keys in `relations`/`functions`) —
neither one removes a relation the `types` registry's own transitive closure still needs to
resolve a `"composite"` entry's `relation` key. A composite type's backing relation is always
present in `relations` under its own key once anything exported references it, regardless of its
schema or the blacklist — the same way `database.ts` unconditionally inlines a referenced
composite/domain/enum's body.

## Top-level shape

```ts
interface DatabaseJson {
  version: 1
  search_path: string[]
  types: Record<string, TypeInfo>
  relations: Record<string, RelationInfo>
  relationships: Record<string, RelationshipVariant[]>
  functions: Record<string, FunctionInfo[]>
  functions_by_name: Record<string, string>
  computed_properties: Record<string, Record<string, ComputedFieldInfo>>
  wellknowns: Record<string, WellknownInfo>
}
```

Every `Record` above is keyed `"schema.name"`, except `types` (keyed the same way, but never
schema-filtered — see [`types`](#types) below) and `wellknowns` (keyed by the well-known query's
own declared name). Every entry that lives under a `"schema.name"` key also repeats its own
`schema`/`name` fields, so you never have to split the map key back apart to know what you're
looking at.

`version` is a schema-format version for this JSON payload itself, bumped whenever a breaking
shape change is made to any section below — unrelated to the database's own schema version.

`search_path` is the database's search path, in lookup order. It's what `functions_by_name`'s
winners were resolved against — you need it too if you're re-deriving unqualified-name
resolution yourself.

## `types`

```ts
type TypeInfo = { schema: string; name: string } & (
  | { kind: "scalar"; comment?: string }
  | { kind: "enum"; comment?: string; labels: string[] }
  | { kind: "domain"; comment?: string; base: string; not_null: boolean }
  | { kind: "array"; element: string }
  | { kind: "composite"; comment?: string; relation: string }
)
```

Keyed `"schema.name"` (a scalar built-in like `pg_catalog.int4` included). One entry per type
*referenced*, directly or transitively, by a column or function argument/return type across
every relation/function this response exports, regardless of which schema that type itself lives
in — a column in an exported schema can be typed by a composite/domain/enum declared in one that
isn't otherwise exported.

`"scalar"` covers every built-in Postgres type with no further structure — `kind` alone tells you
there's nothing more to resolve. `"domain"` names its `base` type by key (recurse through `base`
for a domain-of-domain) and carries `not_null` separately from any given column's own
nullability — a domain's not-null constraint applies regardless of what a column built on it
reports. `"composite"` names its backing relation by key into `relations` instead of repeating
its column list — a composite type's shape is always exactly its relation's own `columns`.

> **Why:** a registry keyed by type identity, referenced by string rather than inlined, means a
> type used by a hundred columns is described once. It also means you can resolve a type
> reference without caring which section it came from — column, function argument, or return
> type all point into the same `types` map.

## `relations`

```ts
interface RelationInfo {
  schema: string
  name: string
  kind: "table" | "view" | "materialized_view" | "type"
  comment?: string
  columns: ColumnInfo[]
  primary_key: { name: string; columns: string[] } | null
  unique_constraints: { name: string; columns: string[] }[]
  indexes: IndexInfo[]
}

interface ColumnInfo {
  name: string
  type: string
  comment?: string
  is_nullable: boolean
  is_primary_key: boolean
  is_identity: boolean
  is_generated: boolean
  is_updatable: boolean
  has_default: boolean
}

interface IndexInfo {
  name: string
  unique: boolean
  columns: string[]
}
```

Keyed `"schema.name"`. Every relation resolves here except a function's own synthetic row-type
relation, which is inlined on that function instead of getting its own `relations` entry — see
[`functions`](#functions-functions_by_name) below. `kind` is `"type"` for a standalone composite
type's own pseudo-relation — exactly the relation a `types` entry's `"composite"` `relation` key
points to, so it's reachable both ways: through the type registry, or directly here if you only
care about shape.

The relation's own `comment` and each column's own `comment` are their respective `COMMENT ON
TABLE`/`COMMENT ON COLUMN` text — the same source `database.ts` surfaces as a `/** ... */` doc
comment — omitted entirely when unset, never an empty string.

`columns` is declared order. `type` is a key into `types`. `is_nullable` is the raw introspected
value (`information_schema.columns.is_nullable`); a domain's own `not_null`
(`types[column.type].not_null` when `type`'s `kind` is `"domain"`) is a *separate* fact you have
to combine yourself — `database.json` never pre-collapses the two the way `database.ts`'s column
typing does. `has_default` is whether the column has a default expression; the expression text
itself isn't exposed (arbitrary SQL, not meant for a JS consumer to parse or re-run).

`primary_key` names the relation's own PRIMARY KEY constraint and its ordered column list, `null`
when it has none. `unique_constraints` lists every UNIQUE constraint the same way (the primary
key is never repeated there). `indexes` lists every index found on the relation; `columns` is the
true declared key-column order with INCLUDE-only columns already excluded — join eligibility
elsewhere in this document (`relationships`) is decided by whether a column set is covered by
some leading prefix of one of these, so you can re-derive that coverage yourself instead of it
being restated per relationship.

## `relationships`

```ts
interface RelationshipVariant {
  target: string
  direction: "outgoing" | "incoming"
  on: Record<string, string>
  unique: boolean
  eligible: boolean
  shortcut: string
  constraint_name: string
}
```

Keyed `"schema.name"` of the owning relation, one array entry per declared foreign key, in both
directions — every foreign key touching this relation gets a variant here, whether or not it's
actually usable in a `join()`. `target` is the key of the other side's entry in `relations`.
`direction` is `"outgoing"` when this relation itself holds the FK columns (`on`'s keys, pointing
at `target`'s own PRIMARY KEY/UNIQUE columns), `"incoming"` when `target` holds them instead
(pointing back at this relation). `on` maps this relation's own column names to the corresponding
column name on `target`, in the direction implied above — the same shape the query language's own
join `"on"` field already takes (e.g. `{"director_id": "id"}`), so you can build a `join` clause
directly from it without parsing anything. `unique` is whether following `on` from this relation
to `target` yields at most one row — always `true` for `"outgoing"` (an FK's target columns are
always PRIMARY KEY/UNIQUE-backed), and for `"incoming"` only when this relation's own FK columns
are themselves unique (a 1:1 reverse relationship, rare). `constraint_name` is the backing
foreign key's own name — the same name on both directions' variants, since they're the two sides
of one constraint.

`eligible` is `false` when the FK-holding side's own `on` columns aren't indexed (`database.ts`'s
own relationships silently drop these entirely); `shortcut` is still the correct join-syntax
string in that case, it just isn't safely usable in a live `join()` — the query engine rejects it
at request time. A relation with no foreign key in either direction is simply absent from this
map, never present with an empty array.

> **Why:** an automatic admin needs every declared relationship to render a linked-record picker
> or a related-records tab, not only the ones the query engine can efficiently join — an
> unindexed FK is still a real, navigable relationship, just one the admin has to resolve with
> its own lookup instead of a `join()`. Filtering it out entirely, the way `database.ts` does,
> would silently hide part of the schema from that use case.

## `functions` / `functions_by_name`

```ts
interface FunctionInfo {
  schema: string
  name: string
  comment?: string
  volatility: "immutable" | "stable" | "volatile"
  is_strict: boolean
  args: FunctionArgInfo[]
  returns_set: boolean
  returns: string | null
  relation?: string
}

interface FunctionArgInfo {
  name: string
  type: string
  mode: "in" | "out" | "inout" | "variadic" | "table"
  has_default: boolean
}
```

`functions` is keyed `"schema.name"`, one array entry per overload (Postgres allows several
functions to share a name, distinguished by argument list) — plain functions only, same
blacklist as `database.ts`. `volatility`/`is_strict` are collapsed from Postgres' own
immutable/stable/volatile flags (exactly one is ever true) and its strictness flag — useful for
deciding whether a call is safe to cache or repeat. `args` lists every argument in declaration
order, `mode` carrying Postgres' own IN/OUT/INOUT/VARIADIC/TABLE distinction verbatim — unlike
`database.ts`, which only ever renders the callable IN/INOUT/VARIADIC subset, `database.json`
keeps OUT and TABLE-mode arguments too, since a consumer working with HTTP routes needs the full
argument shape, not just the callable one. `has_default` is only ever true for a trailing
IN/INOUT argument; always `false` for `"out"`/`"table"`/`"variadic"` arguments.

`returns` is a key into `types` for a scalar/enum/domain/composite/array return type. It's `null`
exactly when the function has no real backing return type (a RETURNS TABLE/OUT-parameter
function sharing Postgres' generic `record` pseudo-type) — in that case the row shape is exactly
this same function's own `args` entries whose `mode` is `"out"` or `"table"`, in declaration
order, never duplicated into a separate shape. `relation` is present only when `returns_set` is
true and the function's return composite type resolves to a real, exported relation (a key into
`relations`).

`functions_by_name` maps a bare (unqualified) function name to the one `"schema.name"` key in
`functions` an unqualified call resolves to via `search_path`. A name whose only candidates all
live outside `search_path` is omitted, matching Postgres' own resolution.

## `computed_properties`

```ts
type ComputedProperties = Record<string, Record<string, ComputedFieldInfo>>

interface ComputedFieldInfo {
  returns: string
  returns_set: boolean
}
```

Outer key is the owning relation's `"schema.name"`, inner key the computed field's own name —
filtered to non-blacklisted functions, the same set `database.ts` documents for `select`
authoring. See [Computed fields](../query-language/computed-fields.md). `returns` is a key into
`types` (never `null` — a computed field's function is always callable with exactly one argument
and typed on the relation's own composite row, so it never falls into the record-shape case
`functions.returns` can). A relation with no computed fields is absent from the outer map.

## `wellknowns`

```ts
interface WellknownInfo {
  name: string
  query: unknown
  params: WellknownParamInfo[]
}

interface WellknownParamInfo {
  name: string
  pg_type: string
  kind: "number" | "string" | "boolean" | "unknown"
  has_default: boolean
}
```

Keyed by the [well-known query's](../query-language/well-known-queries.md) own declared name
(also repeated as `name`). `query` is the compiled query JSON verbatim — the same value
`database.ts` embeds as a `const ... as const` literal — so a consumer not resolving types
through `types`/`relations` can still read the raw query structure directly. `params` lists the
query's declared parameters; `pg_type` is the declared param's own type string verbatim (an
arbitrary author-written Postgres type name, e.g. `"int"`, never resolved against the
introspected `types` registry), and `kind` is the same coarse bucket used for the wire-level type
check — not a `types` key, since a well-known param's declared type was never introspected from a
real column or argument in the first place.
