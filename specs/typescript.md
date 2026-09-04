# Javascript/Typescript interactions

At its core, rel was thought up to be queried by javascript clients ; web browsers. JSON is its query language because it plays particularly nice with it.

## Goals

Especially for typescript ; provide a lightweight, ergonomic and minimalist client library along with database schema in JSON format that can :

- Provide completion while writing queries, especially on column/property names so that changing names get flagged
- Give a way to deserialize cleanly into objects with specific prototypes (in particular to allow custom accessors and methods) ; embedded children must retain prototype and type information for the result at the call site. custom prototypes must also allow for a post-deserialization hook.
- Give a way to serialize back those objects, even when creating prototypes.
- Type check column usage in expressions (might be hard and will use a lot of generics - probably a v2 concern)
- Give a json schema of the database that could be introspected by a library

Not an ORM — `.save()` is a non-goal. What matters is that a query's shape and result are known to TypeScript.

A facility to `setPrototypeOf()` transparently, so a developer can add accessors/methods to returned types and their embedded sub-types, is a primary goal. It must be minimally intrusive enough to survive regeneration of the file, and must surface an error if a change no longer applies.

Generated code may output comments telling biome/prettier to deactivate checks it might not conform to.

> Thoughts: footprint should be minimal, done almost entirely in typespace — importing a schema augments a helper function that shapes the result, rather than generating bulky per-schema code.
>
> A separate mechanism should give the querier a JSON listing of whatever they can query, schema-whitelisted (no `pg_catalog` by default, though a referenced type should still pull its definition in) — introspection output in a "palatable," less pg-flavored form (especially function arguments).

## Configuration

- `http.typescript.enable` (default false, true if dev enabled) enable serving typescript files of the server
- `http.typescript.schemas` (default empty) a whitelist of schemas that can be asked
- `http.json.enable` (default false, true if dev enabled) enable serving the JSON file describing the database
- `typescript.helper_path` (default empty, disabled) : a filesystem path rel writes `database.ts`'s content to directly, on top of serving it over HTTP. Meant for local development — an editor/LSP watching a real file on disk, without a request round-trip.

## Reloading `helper_path`

When `typescript.helper_path` is set, rel (re)writes the file at that path at startup, and again on every `SIGUSR1` reload, as an added step of `specs/migrations.md ## Reloading`'s sequence — right after step 4 (reintrospection), using the same freshly reintrospected `pg.DbInfos` step 5's registry rebuild consumes. If step 3/4 fails (dmut or introspection error), the file is left untouched, same as the schema/registry themselves — the reload aborts before this step is ever reached.

The write is a plain overwrite of the whole file ; there is no incremental/diffed update.

## Endpoints

Available as `GET`, both endpoints accept `schemas` as a query parameter that allows selecting which schemas will be included into the file. If not provided, all known schemas (aside from `pg_catalog`) are included into the output.

- `/rel/database.ts` : the database described as typescript types with some helpers
- `/rel/database.json` : a JSON file that describe the database exported by rel


## database.json

Based on the introspection we made, outputs a full JSON file

## database.ts

The typescript file is somewhat different from the json file ; on top of serving the structure of the database, it also provide a few helper functions that make use of the generated types to help the developper.

A draft lives under `./typescript/` : `query.ts` (`RelationQuery`/`Expression`, the wire format), `shapes.ts` (the Shape-inference machinery), `querier.ts` (`Querier`/`relation()`/`func()`/`join()`/`wellknown()`), and `schema.example.ts` (a worked "hotel" example standing in for section 2, below — a real deployment generates that section from introspection instead). `example.ts` exercises all of it together and is `## Testing`'s answer, below. `tsconfig.json`/`biome.json` keep the whole directory checkable as one project (`just check`) while it's still hand-edited.

The generated file is self-sufficient : a single `.ts` file with no imports, produced by concatenating `querier.ts` + the generated schema + `query.ts` + `shapes.ts`, each with its own local `import ... from "./..."` line (needed for `./typescript/*.ts` to type-check as separate files today) stripped first — so it can be dropped in and used (or pointed at by `typescript.helper_path`, above) entirely on its own.

### File layout

Three sections, always in this order :

1. **Actual code** (`querier.ts`) : `Querier`, `relation()`, `func()`, `join()`, `wellknown()` — the hand-written-style helpers a developer calls directly.
2. **The database schema**, as introspected : `Relations`, `Relationships`, `Functions`, `Wellknowns`, and the `Table__`/`View__`/`Type__` interfaces they reference. Generated fresh per deployment (`## Schema interfaces`, below) ; `schema.example.ts` is what this section looks like for the repo's own worked example.
3. **`RelationQuery` and its supporting type-level machinery** (`query.ts` + `shapes.ts`) : `Expression`, `ShapeFromQuery` and the rest of the Shape-inference helpers.

> Why: a developer opening the file is checking behavior (section 1) or their own schema (section 2) far more often than the inference plumbing that makes both work (section 3) — the order matches that reading priority, not dependency order. TypeScript itself doesn't care about declaration order for types, so this ordering costs nothing.

### Schema interfaces

**Naming.** `Table__<Schema>__<Relation>` (one per introspected table), `View__<Schema>__<Relation>` (one per introspected view, same column rules as `Table__`), `Type__<Schema>__<Name>` (one per introspected composite/enum/domain type referenced by a column or function argument — an enum becomes a string-literal union, a composite an interface with the same column rules as `Table__`). `<Schema>`/`<Relation>`/`<Name>` are the introspected identifier, PascalCased at each `_` boundary (`room_types` → `RoomTypes`) ; the `__` separator disambiguates schema/relation regardless of underscores already inside either part.

**Column typing.** Every column is present on the interface — reading a row always returns every selected column — so nullability is expressed as a `T | null` union, never as an optional (`?`) property ; a column's `DEFAULT` doesn't affect its read type at all (it only matters for `insert_columns`/write behavior, unrelated to this interface).

**`Relations`** : `{ "schema.relation": Table__Schema__Relation | View__Schema__Relation }`, one entry per introspected, schema-whitelisted table or view, keyed by the same `"schema.relation"` string `relation()`/a join's `relation`/`schema` fields use. `shapes.ts`'s `ResolveModel` is the sole reader.

**`Relationships`** : one entry per relation with at least one joinable link, keyed by that relation's own `"schema.relation"` name ; the value is a UNION of variants, one per eligible FK reachable from that relation, discriminated by each variant's own literal `shortcut` string. A relation with several FKs (to the same or different targets) gets several variants under its one key instead of colliding — `shortcut` alone, not the key, is what a caller ultimately picks. FK eligibility is `query.ts`'s own `on` doc comment : "Limited to FK-backed columns, or unique-backed distant columns" — a FK to a non-unique column produces no variant at all, since it isn't a legal `on` target either. Each eligible FK produces up to two variants, one per direction, filed under different keys : `unique: true`, `>`-prefixed shortcut, filed under the owning (FK-holding) relation ; `unique: false`, `<`-prefixed shortcut, filed under the referenced relation — see `schema.example.ts`'s own doc comment on the interface for the exact shortcut-string grammar, and its two-variant `"hotel.rooms"` entry for a worked multi-FK example. No runtime companion is needed : `join(key, shortcut, request)` takes the literal `shortcut` string as its own argument and parses it directly, rather than looking it up from something keyed by `key` alone.

**`Functions`** : `{ "schema.function": { positional_args, args, returns, relation? } }`, one entry per introspected, schema-whitelisted function. `positional_args` mirrors the pg function's parameter list in declaration order (as a tuple, named for readability) ; `args` is the same parameters as a named object, for call sites that prefer `{ name: value }` over positional. `relation` is present only when the function is set-returning with a row shape matching a real `Table__`/`View__` (`RETURNS SETOF <relation>` or an equivalent `RETURNS TABLE(...)`) — that's what lets `shapes.ts`'s `ResolveFunctionModel` treat the function as embeddable/joinable via `func()`, the same as a table. Absent, the function is scalar and only reachable through `returns` — `func()` still resolves (falling back to the permissive default row, `shapes.ts`'s `ResolveCalledFunctionModel`), but a scalar function is better called through an `Expression`'s `["call", ...]` tag than through `func()`'s select/join machinery.

**`Wellknowns`** : reserved, generated empty until well-known queries are introspectable server-side (`specs/migrations.md`'s well-known section : not implemented as of that document).

## Testing

Type assertions in a dedicated file (`typescript/example.ts`), checked by `just check` (`bunx tsc --noEmit`) : it builds real queries against `schema.example.ts` through `relation()`/`join()`/`func()` and exports each one's inferred `Shape` type, so a change to `shapes.ts`'s resolvers that silently loosens or narrows an existing query's shape shows up as a diff there, without needing a live database or a running server. It deliberately never calls `.get()`/`.write()` (those perform a real `fetch()`), so it's also safe to actually import/run, not just type-check.

Not covered yet : anything that needs a live server response (deserialization, `setPrototypeOf()`, the read/write shape asymmetry) — those need an integration test against a running rel instance once that machinery exists, not a pure type-assertion file.
