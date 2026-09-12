// Draft/example of `specs/typescript.md ## File layout`'s section 2 — the database schema. A real deployment
// gets this section generated dynamically from introspection and inserted between querier.ts and query.ts/
// shapes.ts (never written to disk as its own file) ; this "hotel" fixture is kept in the repo so querier.ts/
// shapes.ts have something real to type-check and exercise against (see example.ts).
//
// A type-only cycle with shapes.ts (which itself imports Functions/Relationships/Wellknowns etc. from here) : legal
// in TS, since types are erased before this matters at runtime.
import type { ResolveModel, RootShapeFromLiteralQuery, WriteShapeFromRelationQuery } from "./shapes"

// A bare `{}` type accepts anything non-null (biome's noBannedTypes) ; tsgen generates this instead for an
// actually-empty object shape (a zero-argument function's own `args`, a zero-column relation/composite type —
// `CREATE TABLE t()` is legal Postgres). NOT used for Wellknowns/Functions/FunctionsByName/Relations/
// Relationships/ComputedProperties themselves even when empty — see this session's own design discussion ;
// those keep a literal, biome-ignored empty interface because their `keyof` is load-bearing (`F extends keyof
// Functions`, ...), and `keyof Record<string, never>` is `string`, not `never`.
type EmptyObject = Record<string, never>

// Placeholder for Postgres' `point` type until a real pg_types module exists — specs/typescript.md doesn't cover
// non-trivial pg type mappings yet.
type Point = { x: number; y: number }

// A pg enum : Type__<Schema>__<Name>, same as Table__/View__.
type Type__Hotel__RoomStatus = "clean" | "dirty"

interface Table__Hotel__Rooms {
  /* same goes for columns */
  property_id: number // not null references hotel.properties (id),
  room_type_id: number // not null references hotel.room_types (id),
  room_number: string
  floor: number | null
  status: Type__Hotel__RoomStatus // default 'clean', //: should we handle default creation client-side ?
  features: string[] // default '{}'
}

interface Table__Hotel__Properties {
  /** And comments for each column are added */
  id: number
  chain_id: number | null
  name: string
  location: Point //: this type does not really have a direct equivalent, probably that it will have to be created in some pg_types.ts file
  star_rating: number | null
  description: string | null
  created_at: Date | null //: do we do Temporal directly or do we keep Date ? It could be interesting to have it as a requirement ? (or at least a polyfill)
}

// A second relation `hotel.rooms` has a FK into, alongside `hotel.properties` — exists specifically so
// `Relationships["hotel.rooms"]` below has two variants to disambiguate, not just one.
interface Table__Hotel__RoomTypes {
  id: number
  name: string
  base_price: number
}

export interface Relations {
  "hotel.rooms": Table__Hotel__Rooms
  "hotel.properties": Table__Hotel__Properties
  "hotel.room_types": Table__Hotel__RoomTypes
}

// specs/required-fields.md : the physical columns a write MUST supply a value for — not nullable, no default,
// not identity/generated. `id` is excluded everywhere here (`serial`/identity in the real schema) ; `star_rating`/
// `chain_id`/`location`/`description`/`created_at`/`floor`/`status`/`features` are all either nullable or
// defaulted, so they're excluded too — see shapes.ts's WriteOwnShape/WriteShapeFromExpressionMap for how this
// gets applied.
export interface RequiredColumns {
  "hotel.properties": "name"
  "hotel.rooms": "property_id" | "room_type_id" | "room_number"
  "hotel.room_types": "name" | "base_price"
}

// `shortcut` is interpreted client-side by join() (querier.ts) to fill in `on:`/`relation:`/`schema:` and is
// never sent to the server. Its purpose is limited to provide auto-completion to the developer.
// `on:` alone can't tell TypeScript whether the embed is an object or an array ; `unique` does.
// The pair after `;` is always `<joined relation's column>:<enclosing relation's column>`, matching `on:`'s own
// key/value convention regardless of which side owns the foreign key. `>`/`<` says which side owns the FK.
//
// A key's value is a UNION of variants, one per FK reachable from that relation, discriminated by `shortcut` —
// `hotel.rooms` has two FKs (properties, room_types), so it has two ; `join()` (querier.ts) takes both the key
// and the specific `shortcut` to pick exactly one, so a relation with several FKs never collides on one shape.
export interface Relationships {
  "hotel.rooms":
    | {
        shortcut: "hotel.properties>;id:property_id" // rooms.property_id -> properties.id
        unique: true // true : single object, false : array
        relation: Table__Hotel__Properties // Extract<Relationships[key], {shortcut}>["relation"] is what ResolveModel resolves this join's row shape to
      }
    | {
        shortcut: "hotel.room_types>;id:room_type_id" // rooms.room_type_id -> room_types.id
        unique: true
        relation: Table__Hotel__RoomTypes
      }
  "hotel.properties": {
    shortcut: "hotel.rooms<;id:property_id" // rooms.property_id -> properties.id, viewed from properties
    unique: false
    relation: Table__Hotel__Rooms
  }
  "hotel.room_types": {
    shortcut: "hotel.rooms<;id:room_type_id" // rooms.room_type_id -> room_types.id, viewed from room_types
    unique: false
    relation: Table__Hotel__Rooms
  }
}

// Discoverability half of computed columns (specs/typescript.md ; query-engine.md ## Reading Algorithm's own
// definition) : per relation, which BARE function names are callable against it as a zero-extra-argument
// property (`t.func_name()`/`func_name(t)`), and what each returns. Deliberately redundant with FunctionsByName
// below rather than derived from it — see this session's own design discussion (tsgen's renderComputedProperties
// doc comment carries the full reasoning). property_average_rating's SECOND overload (below) takes `number`, not
// `Table__Hotel__Properties`, as its first argument, so it's excluded here even though the bare name is shared.
interface Computed__Hotel__Properties {
  property_average_rating: number
}

export interface ComputedProperties {
  "hotel.properties": Computed__Hotel__Properties
}

// For each function that is exported, we get an export
export interface Functions {
  // Overloaded (two entries sharing this key, unioned) : same name, different
  // signature. The second variant is contrived purely to regression-test
  // shapes.ts's ResolveFunctionModel against a MIXED scalar/relation overload
  // set — see example.ts's own assertions on this key.
  "hotel.property_average_rating":
    | {
        positional_args: [Table__Hotel__Properties]
        args: { property: Table__Hotel__Properties }
        returns: number
      }
    | {
        positional_args: [property_id: number]
        args: { property_id: number }
        relation: Table__Hotel__Properties
        returns: Table__Hotel__Properties[]
      }
  "hotel.rooms_available": {
    positional_args: [property_id: number, on_date?: Date]
    args: { property_id: number; on_date?: Date }
    relation: Table__Hotel__Properties // this function has an underlying
    returns: Table__Hotel__Properties[]
  }
  // Zero-argument function : `args` falls back to EmptyObject, never a literal `{}` — see EmptyObject's own doc
  // comment, and example.ts's own assertion on this key.
  "hotel.property_count": {
    positional_args: []
    args: EmptyObject
    returns: number
  }
}

// Bare (unqualified, search_path-resolved) name -> Functions entry ; shapes.ts's ShapeFromCallTag consults this
// so `["call", "property_average_rating", ...]` — Postgres' own `t.func_name()` computed-column calling
// convention — type-resolves without spelling out `{schema:"hotel", name:"property_average_rating"}` every time.
export interface FunctionsByName {
  property_average_rating: Functions["hotel.property_average_rating"]
  rooms_available: Functions["hotel.rooms_available"]
}

// Well-known queries (specs/typescript.md ## Wellknowns) : each compiled query's own raw "query" JSON is embedded
// verbatim as a `const ... as const` literal, and ShapeFromRelationQuery/WriteShapeFromRelationQuery (shapes.ts)
// infer its shape from that literal directly, rather than re-deriving it in Go — the UNCONSTRAINED entry points,
// not ShapeFromQuery/WriteShapeFromQuery : `as const` makes every nested array/tuple readonly, which the
// constrained `Q extends RelationQuery<...>` signature rejects (its own where/select fields are typed as mutable
// tuples).
const __wellknown_properties_by_star_rating_query = {
  schema: "hotel",
  relation: "properties",
  where: ["=", "star_rating", ["$param", "min_rating", "int"]],
} as const

export interface Wellknowns {
  properties_by_star_rating: {
    params: { min_rating: number }
    // Root cardinality (shapes.ts ## Root cardinality) : this query is relation-rooted, so its response is
    // unconditionally an array of rows — RootShapeFromLiteralQuery covers the function-rooted case too, for a
    // well-known query built on a set-returning or scalar function instead.
    shape: RootShapeFromLiteralQuery<typeof __wellknown_properties_by_star_rating_query>
    // Same root-array wrap as `shape` above — writing.md : "data" is "an array of rows at the root". A
    // function-rooted well-known query isn't writable at all (query/write.go's CompileSelectForDataNode rejects
    // any function-rooted node), so this straightforward `[]` wrap — rather than going through
    // RootShapeFromLiteralQuery's own function-vs-relation dispatch — is only ever exercised by a relation root.
    write_shape: WriteShapeFromRelationQuery<
      typeof __wellknown_properties_by_star_rating_query,
      ResolveModel<typeof __wellknown_properties_by_star_rating_query>
    >[]
  }
}
