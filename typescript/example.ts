// Compile-only fixture exercising relation()/join()/func() against the "hotel" example schema
// (schema.example.ts). specs/typescript.md ## Testing : until a proper runtime test harness exists, this file's
// only job is to fail `just check` if the generic machinery in querier.ts/shapes.ts stops accepting a
// well-formed query, or silently loosens some field's inferred type. Never calls .get()/.write() — those hit a
// real `fetch()` — so this file is safe to simply import, not just type-check.

import { func, join, relation, wellknown } from "./querier"
import type { Functions } from "./schema.example"
import type { ResolveModel } from "./shapes"

const properties = relation("hotel.properties", {
  select: {
    col: "created_at",
    test: ["$param", "toto", "string"],
  },
  join: {
    // root is "hotel.properties", so the key names *this* side of the relationship, not the target :
    // Relationships["hotel.properties"] is the properties -> rooms (one-to-many) entry — only one variant, so
    // there's only one valid shortcut to pick here.
    rooms: join("hotel.properties", "hotel.rooms<;id:property_id", {
      select: ["full"],
    }),
  },
})

// Exported so a change to ShapeFromQuery/ResolveModel that silently loosens/narrows this query's inferred shape
// shows up as a diff here, not just a passing compile.
export type PropertiesShape = Awaited<ReturnType<typeof properties.get>>

const rooms = relation("hotel.rooms", {
  join: {
    // "hotel.rooms" has TWO FKs (properties, room_types) — Relationships["hotel.rooms"] is a union, so `join()`
    // needs the explicit shortcut to pick between them ; this is exactly the case ## Schema interfaces' old
    // `> Question:` block was about.
    property: join("hotel.rooms", "hotel.properties>;id:property_id", {
      select: ["own"],
    }),
    room_type: join("hotel.rooms", "hotel.room_types>;id:room_type_id", {
      select: ["own"],
    }),
  },
})

export type RoomsShape = Awaited<ReturnType<typeof rooms.get>>

// Scoped-join callback form (specs/typescript-better-join.md) : equivalent to `properties` above, but the
// `join` handed to the callback is already scoped to "hotel.properties", so nested join() calls never repeat it.
// Nested two levels deep (properties -> rooms -> room_type) to exercise TargetRelationName's recursive scoping.
const propertiesScoped = relation("hotel.properties", (join) => ({
  select: {
    col: "created_at",
    test: ["$param", "toto", "string"],
  },
  join: {
    rooms: join("hotel.rooms<;id:property_id", (join) => ({
      select: ["full"],
      join: {
        room_type: join("hotel.room_types>;id:room_type_id"),
      },
    })),
  },
}))

export type PropertiesScopedShape = Awaited<ReturnType<typeof propertiesScoped.get>>

// No second argument at all : a bare select-all, per resolveRequest()'s default (querier.ts).
const propertiesBare = relation("hotel.properties")

export type PropertiesBareShape = Awaited<ReturnType<typeof propertiesBare.get>>

const roomsAvailable = func("hotel.rooms_available", {
  select: ["full"],
})

export type RoomsAvailableShape = Awaited<ReturnType<typeof roomsAvailable.get>>

// get/set read-vs-write asymmetry (shapes.ts's WriteShapeFromQuery doc comment) : `audit_only` (`set`) must be
// writable but never read back ; `display_only` (`get`) is the reverse. `write`'s overloaded signature means
// `Parameters<...>` picks its last (single-arg) overload, giving WriteShape directly without a separate import.
const propertyAudit = relation("hotel.properties", {
  select: {
    id: "id",
    audit_only: ["set", "chain_id"],
    display_only: ["get", "star_rating"],
  },
})

export type PropertyReadShape = Awaited<ReturnType<typeof propertyAudit.get>>
export type PropertyWriteShape = Parameters<typeof propertyAudit.write>[0]

type Expect<T extends true> = T
type HasKey<T, K extends string> = K extends keyof T ? true : false

export type _AssertReadHasDisplayOnly = Expect<HasKey<PropertyReadShape, "display_only">>
export type _AssertReadOmitsAuditOnly = Expect<
  HasKey<PropertyReadShape, "audit_only"> extends false ? true : false
>
export type _AssertWriteHasAuditOnly = Expect<HasKey<PropertyWriteShape, "audit_only">>
export type _AssertWriteOmitsDisplayOnly = Expect<
  HasKey<PropertyWriteShape, "display_only"> extends false ? true : false
>

// shapes.ts's ResolveFunctionModel/DistributeOverload : "hotel.property_average_rating" is overloaded
// (schema.example.ts) with a scalar (no `relation`) variant and a set-returning (`relation:
// Table__Hotel__Properties`) variant — each overload must resolve through its OWN branch (scalar -> its
// `returns`, set-returning -> its unwrapped `relation`, not the raw `returns` array) rather than the whole
// union collapsing onto whichever single branch every member happens to satisfy.
type PropertyAverageRatingResolved = ResolveModel<{ function: "hotel.property_average_rating" }>

// Distributive : only a raw array member (the bug this regression catches — `returns`'s own `Rel[]` leaking
// through unwrapped) contributes `true` ; a bare object row does not.
type ContainsArrayMember<T> = T extends readonly unknown[] ? true : never
// Distributive : only the set-returning variant's unwrapped relation object matches `{ id: number }`.
type ContainsIdKeyedMember<T> = T extends { id: number } ? true : never

export type _AssertOverloadKeepsScalarMember = Expect<
  Extract<PropertyAverageRatingResolved, number> extends never ? false : true
>
export type _AssertOverloadUnwrapsRelationMember = Expect<
  ContainsIdKeyedMember<PropertyAverageRatingResolved>
>
export type _AssertOverloadResolutionNeverLeaksRawArray = Expect<
  ContainsArrayMember<PropertyAverageRatingResolved> extends never ? true : false
>

// shapes.ts's ShapeFromCallTag : a computed column selected via ["call", ...] must type through its declared
// `returns` — bare (unqualified, FunctionsByName) and schema-qualified ({schema,name}, Functions directly) forms
// both exercised, since the whole point of the bare form is NOT having to spell out {schema,name} every time.
const propertyWithComputed = relation("hotel.properties", {
  select: {
    id: "id",
    avg_bare: ["call", "property_average_rating", "id"],
    avg_qualified: ["call", { schema: "hotel", name: "property_average_rating" }, "id"],
  },
})

export type PropertyWithComputedShape = Awaited<ReturnType<typeof propertyWithComputed.get>>
export type PropertyWithComputedWriteShape = Parameters<typeof propertyWithComputed.write>[0]

// property_average_rating is overloaded (schema.example.ts) ; ReturnsOf deliberately does NOT unwrap the
// set-returning variant's `relation` the way ResolveFunctionModel does for func()'s embed path — a "call" tag
// selects one JSON value, so its array-shaped `returns` should come through as an array, unlike
// PropertyAverageRatingResolved's own assertions above. Only the scalar member is checked here ; the point is
// that `number` is reachable at all (it wouldn't be, pre-fix, since ["call", ...] fell through to `unknown`
// entirely).
export type _AssertCallBareNameResolvesFunctionReturns = Expect<
  Extract<PropertyWithComputedShape["avg_bare"], number> extends never ? false : true
>
export type _AssertCallQualifiedResolvesFunctionReturns = Expect<
  Extract<PropertyWithComputedShape["avg_qualified"], number> extends never ? false : true
>
// A computed column is never writable (query-engine.md ## Reading Algorithm) — dropped from the write shape
// entirely, same as `get`, rather than kept with a nonsensical `unknown` type.
export type _AssertCallOmittedFromWriteShape = Expect<
  HasKey<PropertyWithComputedWriteShape, "avg_bare"> extends false ? true : false
>

// EmptyObject : hotel.property_count takes zero arguments, so its own `args` must be a GENUINELY empty object
// type — an arbitrary object with real keys must NOT be assignable to it. A literal `{}` would fail this check
// (it accepts anything non-null, keys included), which is exactly why EmptyObject exists instead.
export type _AssertZeroArgFunctionArgsRejectsArbitraryKeys = Expect<
  { foo: "bar" } extends Functions["hotel.property_count"]["args"] ? false : true
>

// Wellknowns (specs/typescript.md ## Wellknowns) : wellknown()'s params are supplied upfront, and its Shape/
// WriteShape come from the raw query literal via ShapeFromRelationQuery/ResolveModel (schema.example.ts) — no
// `.get(params)` argument needed, unlike relation()/func()'s own `$param`-driven Params.
const starRatedProperties = wellknown("properties_by_star_rating", { min_rating: 4 })

export type StarRatedPropertiesShape = Awaited<ReturnType<typeof starRatedProperties.get>>

export type _AssertWellknownShapeHasStarRating = Expect<
  HasKey<StarRatedPropertiesShape, "star_rating">
>
