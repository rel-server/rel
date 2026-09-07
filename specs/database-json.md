# database.json

`GET /rel/database.json` is `docs/content/typescript-client.md ## database.json` — the introspected schema as plain JSON, for a caller that isn't a TypeScript project or wants to feed the schema into its own tooling.

## Endpoint

Same wiring as `server/typescript.go`'s `/rel/database.ts` handler : gated behind `http.typescript.enable`, GET only, and the `schemas` query param intersected with the configured `http.typescript.schemas` whitelist (a `schemas` value outside the whitelist is a 400, not a silent drop). `http.typescript.blacklist` (config.Blacklist) is applied identically — a blacklisted relation/function is absent from every section below, the same as in `database.ts`.

The `schemas` whitelist and the blacklist both decide which relations/functions are exported as first-class, independently-reachable entries (`relations`'/`functions`' own keys) — neither one removes a relation the `types` registry's own transitive closure still needs to resolve a `"composite"` entry's `relation` key (see `types` below). A composite type's backing relation is always present in `relations` under its own key once anything exported references it, regardless of its schema or the blacklist, the same way `database.ts`'s own type collector already inlines a referenced composite/domain/enum's body unconditionally.

Response is `Content-Type: application/json; charset=utf-8`. Every object key is `snake_case`.

> **Why:** `database.ts` collapses everything into a TypeScript type expression, which is exactly the information a non-TS consumer (or a tool that wants to reason about the schema itself, e.g. narrowing raw operators/`agg`/`format` expressions the way `typescript/shapes.ts`'s own `ShapeFromExpression` doc comment flags as unresolved v2 work) can't recover from the .ts file. `database.json` keeps the introspected facts (nullability, domain identity, column defaults) instead of the already-collapsed TS string, so it can serve as that future work's input.

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

Every `Record` above is keyed `"schema.name"` (`relationKey`/`relationInterfaceName`'s own key, without the TS-identifier casing tsgen applies for a type name) except `types`, which is keyed the same way but never schema-filtered — see `types` below — and `wellknowns`, keyed by the well-known query's own declared name. Every entry that lives under a `"schema.name"` key also repeats its own `schema`/`name` fields, so a consumer holding one entry never has to split the map key back apart to know what it's looking at.

`version` is a schema-format version for this JSON payload itself, bumped whenever a breaking shape change is made to any section below ; unrelated to the database's own schema.

`search_path` is `DbInfos.SearchPath` verbatim, in lookup order. It's what `functions_by_name`'s winners were resolved against ; a consumer re-deriving unqualified-name resolution needs it too.

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

Keyed `"schema.name"` (a scalar built-in like `pg_catalog.int4` included). One entry per type *referenced*, directly or transitively, by a column or function argument/return type across every relation/function this response exports — the same transitive-closure walk `tsgen`'s own `typeCollector` performs — regardless of which schema that type itself lives in, since a column in an exported schema can be typed by a composite/domain/enum declared in one that isn't otherwise exported.

`"scalar"` covers every built-in Postgres type with no further structure — `kind` alone tells a consumer there's nothing more to resolve. `"domain"` names its `base` type by key (recurse through `base` for a domain-of-domain) and carries `not_null` (`pg.Type.PgDomainNotNull`) separately from any given column's own nullability — a domain's not-null constraint applies regardless of what a column built on it reports. `"composite"` names its backing relation by key into `relations` instead of repeating its column list — a composite type's shape is always exactly its relation's own `columns`.

> **Why:** a registry keyed by type identity, referenced by string rather than inlined, means a type used by a hundred columns is described once. It also means a consumer can resolve a type reference without caring which section it came from — column, function argument, or return type all point into the same `types` map.

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

Keyed `"schema.name"`. Every `pg.Relation` resolves here except a function's own synthetic `RecordRelation` (`IsSynthetic` — inlined on that function instead, never its own `relations` entry ; see `functions` below). `kind` is `"type"` for a standalone composite type's own pseudo-relation (`IsStandaloneCompositeType`) — exactly the relation a `types` entry's `"composite"` `relation` key points to, so it's reachable both ways : through the type registry, or directly here for a caller that only cares about shape.

The relation's own `comment` and each column's own `comment` are their respective `COMMENT ON TABLE`/`COMMENT ON COLUMN` text (`Relation.Comment`/`Column.Comment`) — the same source `database.ts` surfaces as a `/** ... */` doc comment ; omitted entirely when unset, never an empty string.

`columns` is declared order (`ordinal_position`). `type` is a key into `types`. `is_nullable` is the raw introspected value (`information_schema.columns.is_nullable`) ; a domain's own `not_null` (`types[column.type].not_null` when `type`'s `kind` is `"domain"`) is a *separate* fact a consumer must combine itself — `database.json` never pre-collapses the two the way `database.ts`'s `IsReallyNotNull` does for its own column typing. `has_default` is whether `DefaultExpression` is non-empty ; the expression text itself isn't exposed (arbitrary SQL, not meant for a JS consumer to parse or re-run).

`primary_key` names the relation's own PRIMARY KEY constraint (`Constraint.Name`) and its ordered column list, `null` when it has none. `unique_constraints` lists every UNIQUE constraint the same way (the primary key is never repeated there — it already has its own field). `indexes` lists every index found by `FillIndexInformations` ; `columns` is the true declared key-column order with INCLUDE-only columns already excluded (`pg.Index`'s own doc comment) — join eligibility elsewhere in this document (`relationships`, `ResolveJoin`) is decided by whether a column set is covered by some leading prefix of one of these, so a consumer can re-derive that coverage itself instead of it being restated per relationship.

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

Keyed `"schema.name"` of the owning relation, one array entry per declared foreign key, in both directions — every foreign key touching this relation gets a variant here, whether or not it's actually usable in a `join()`. `target` is the key of the other side's entry in `relations`. `direction` is `"outgoing"` when this relation itself holds the FK columns (`on`'s keys, pointing at `target`'s own PRIMARY KEY/UNIQUE columns), `"incoming"` when `target` holds them instead (pointing back at this relation). `on` maps this relation's own column names to the corresponding column name on `target`, in the direction implied above — the same shape the query language's own join `"on"` field already takes (`query/expression_resolve_test.go`'s `"on": {"director_id": "id"}`), so a consumer can build a `join` clause directly from it without parsing anything. `unique` is whether following `on` from this relation to `target` yields at most one row — always `true` for `"outgoing"` (an FK's target columns are always PRIMARY KEY/UNIQUE-backed), and for `"incoming"` only when this relation's own FK columns are themselves unique (a 1:1 reverse relationship, rare). `constraint_name` is the backing foreign key's own `Constraint.Name` — the same name on both directions' variants, since they're the two sides of one constraint.

`eligible` is `false` when the FK-holding side's own `on` columns aren't indexed (`database.ts`'s own `Relationships` — `tsgen/schema.go`'s `renderRelationships` — silently drops these entirely) ; `shortcut` is still the correct join-syntax string in that case, it just isn't safely usable in a live `join()` (the query engine's own `ResolveJoin` rejects it at request time). A relation with no foreign key in either direction is simply absent from this map, never present with an empty array.

> **Why:** an automatic admin needs every declared relationship to render a linked-record picker or a related-records tab, not only the ones the query engine can efficiently join — an unindexed FK is still a real, navigable relationship, just one the admin has to resolve with its own lookup instead of a `join()`. Filtering it out entirely, the way `database.ts` does, would silently hide part of the schema from that use case.

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

`functions` is keyed `"schema.name"`, one array entry per overload (Postgres allows several functions to share a name, distinguished by argument list) — plain functions only (`Function.IsPlainFunction`), same blacklist as `database.ts`. `volatility`/`is_strict` are `pg.Function`'s own `IsImmutable`/`IsStable`/`IsVolatile` (exactly one is ever true, collapsed into the single `volatility` field) and `IsStrict` — useful for a consumer deciding whether a call is safe to cache or repeat. `args` lists every argument in declaration order, `mode` carrying Postgres' own IN/OUT/INOUT/VARIADIC/TABLE distinction (`pg.FunctionArgument.PgMode`) verbatim — unlike `database.ts`, which only ever renders the callable IN/INOUT/VARIADIC subset, `database.json` keeps OUT and TABLE-mode arguments too, since a consumer working with HTTP routes (`route/classify_shape.go`'s own IN/OUT split) needs the full argument shape, not just the callable one. `has_default` is only ever true for a trailing IN/INOUT argument `PgNargsDefaults` counts ; always `false` for `"out"`/`"table"`/`"variadic"` arguments.

`returns` is a key into `types` for a scalar/enum/domain/composite/array return type. It's `null` exactly when the function has no real backing return type (`RecordRelation` populated instead — a RETURNS TABLE/OUT-parameter function sharing Postgres' generic `pg_catalog.record` pseudo-type) ; in that case the row shape is exactly this same function's own `args` entries whose `mode` is `"out"` or `"table"`, in declaration order — never duplicated into a separate shape. `relation` is present only when `returns_set` is true and the function's return composite type resolves to a real, exported relation (a key into `relations`) — the same condition `renderFunctionReturns` checks before falling back to the record shape above.

`functions_by_name` maps a bare (unqualified) function name to the one `"schema.name"` key in `functions` an unqualified call resolves to via `search_path` — `tsgen`'s own `bareNameWinners`, unchanged. A name whose only candidates all live outside `search_path` is omitted, matching Postgres' own resolution.

## `computed_properties`

```ts
type ComputedProperties = Record<string, Record<string, ComputedFieldInfo>>

interface ComputedFieldInfo {
  returns: string
  returns_set: boolean
}
```

Outer key is the owning relation's `"schema.name"`, inner key the computed field's own name — `pg.Relation.ComputedFields`, filtered to non-blacklisted functions, the same set `database.ts`'s `ComputedProperties` interface documents for `select` authoring. `returns` is a key into `types` (never `null` — a computed field's function is always callable with exactly one argument and typed on the relation's own composite row, so it never falls into the record-shape case `functions.returns` can). A relation with no computed fields is absent from the outer map.

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

Keyed by the well-known query's own declared name (also repeated as `name`, same reasoning as every other section's own `schema`/`name` fields). `query` is the compiled query JSON verbatim (the same value `database.ts` embeds as a `const ... as const` literal) — a consumer not resolving types through `types`/`relations` can still read the raw query structure directly. `params` lists the query's declared parameters ; `pg_type` is the declared param's own `type` string verbatim (`wellknown.ParamDef.Type` — an arbitrary author-written Postgres type name, e.g. `"int"`, never resolved against the introspected `types` registry), and `kind` is the same coarse bucket `wellknown.TSTypeForParam`/`checkParamType` classify it into for the wire-level type check — not a `types` key, since a well-known param's declared type was never introspected from a real column or argument in the first place.

## Documentation

The interface will live in the documentation with appropriate comments for the field and types (when they're not self-explanatory.)
