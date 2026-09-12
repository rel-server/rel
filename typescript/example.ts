// Compile-only fixture exercising relation()/join()/func() against the "hotel" example schema
// (schema.example.ts). specs/typescript.md ## Testing : until a proper runtime test harness exists, this file's
// only job is to fail `just check` if the generic machinery in querier.ts/shapes.ts stops accepting a
// well-formed query, or silently loosens some field's inferred type. Never calls .get()/.write() — those hit a
// real `fetch()` — so this file is safe to simply import, not just type-check.

import { call, func, join, relation, wellknown } from "./querier"
import type { Functions } from "./schema.example"
import type { DefaultRow, ResolveModel, RootShapeFromFunctionMember } from "./shapes"

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

// func()'s own `arguments` field (shapes.ts's DeferredFunctionArgs) : checked against the function's declared
// `positional_args`/`args`, same as call() — but each slot also accepts a `$param` placeholder, since func()
// returns a deferred Querier reused across `.get(params)` calls with different values.
const roomsAvailableParameterized = func("hotel.rooms_available", {
  arguments: {
    property_id: ["$param", "property_id", "number"],
    on_date: ["$param", "on_date", "date"],
  },
  select: ["full"],
})

export type _AssertFuncArgumentsExposesParam = Expect<
  HasKey<Parameters<typeof roomsAvailableParameterized.get>[0], "property_id">
>

// DeferredFunctionArgs (shapes.ts) rejects a func() `arguments` object that doesn't match the function's own
// declared `positional_args`/`args`, same as call()'s FunctionArgs — a wrong key name, a missing required one,
// or an extra key on a zero-argument function.
// @ts-expect-error wrong argument name : "propertyId" isn't "property_id"
void func("hotel.rooms_available", { arguments: { propertyId: 1 } })
// @ts-expect-error missing required argument : neither overload's args is satisfied by `{}`
void func("hotel.property_average_rating", { arguments: {} })
// @ts-expect-error extra key on a zero-argument function's args (EmptyObject)
void func("hotel.property_count", { arguments: { foo: 1 } })

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

// Root cardinality (shapes.ts ## Root cardinality) : relation()'s .get()/.write() are array-shaped at the root
// (writing.md : "an array of rows at the root") — [number] below gets at one row's own shape for the
// per-column assertions that follow.
export type PropertyReadShape = Awaited<ReturnType<typeof propertyAudit.get>>[number]
export type PropertyWriteShape = Parameters<typeof propertyAudit.write>[0][number]

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

// relation()'s own root-array wrap (querier.ts) is exactly what this regression pins — a bare, unwrapped object
// here (the pre-fix shape) would make every assertion above compare against the wrong kind (Array's own
// `keyof`, not a row's), so this catches the bug at its source rather than only downstream.
export type _AssertPropertiesShapeIsArray = Expect<
  PropertiesShape extends readonly unknown[] ? true : false
>
export type _AssertRoomsAvailableShapeIsArray = Expect<
  RoomsAvailableShape extends readonly unknown[] ? true : false
>
// hotel.property_count (schema.example.ts) has no `relation` — a genuinely scalar function — so func()'s root
// shape must stay a bare `number`, never `number[]` ; this is the one exception ## Root cardinality carries.
const propertyCount = func("hotel.property_count", {})
export type PropertyCountShape = Awaited<ReturnType<typeof propertyCount.get>>
export type _AssertScalarFunctionRootIsNotArray = Expect<
  PropertyCountShape extends readonly unknown[] ? false : true
>
export type _AssertScalarFunctionRootIsNumber = Expect<
  PropertyCountShape extends number ? true : false
>

// An unrecognized function name resolves to `never` (RootShapeFromLiteralQuery/func()'s own F-driven lookup, both
// falling back the same way ResolveModel does elsewhere) — RootShapeFromFunctionMember's own `[M] extends
// [never]` guard exists specifically so this still lands on the safe DefaultRow[] default instead of `M extends
// {...}` distributing over `never` and collapsing the whole conditional to `never` itself.
export type _AssertUnrecognizedFunctionFallsBackToDefaultRowArray = Expect<
  RootShapeFromFunctionMember<never, Record<string, never>> extends DefaultRow[] ? true : false
>

// The fallback above only matters once F has reached the machinery ; relation()/func()/wellknown() each
// constrain their own first argument to a known name (RelationName/FunctionName/keyof Wellknowns), so an
// unrecognized name is rejected at the call site itself, before any of that machinery runs.
// @ts-expect-error unrecognized relation name
void relation("hotel.does_not_exist")
// @ts-expect-error unrecognized function name
void func("hotel.does_not_exist", {})
// @ts-expect-error unrecognized well-known query name
void wellknown("does_not_exist", {})

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

// [number] : same root-array unwrap as PropertyReadShape/PropertyWriteShape above.
export type PropertyWithComputedShape = Awaited<ReturnType<typeof propertyWithComputed.get>>[number]
export type PropertyWithComputedWriteShape = Parameters<
  typeof propertyWithComputed.write
>[0][number]

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

// Required Fields (specs/required-fields.md) : RequiredColumns["hotel.room_types"] names "name"/"base_price" —
// "id" is neither nullable nor required (a serial/identity column in a real deployment), so it must be OPTIONAL
// in the write shape rather than mandatory the way tsgen used to render every physical column, verbatim.
const roomTypeWrite = relation("hotel.room_types", {
  select: ["own"],
})

// [number] : same root-array unwrap as PropertyReadShape/PropertyWriteShape above.
export type RoomTypeWriteShape = Parameters<typeof roomTypeWrite.write>[0][number]

// None of this schema's own column types ever include `undefined` themselves (only `| null` for a genuinely
// nullable SQL column) — so an optional key's own apparent type (T[K] including `| undefined`, added implicitly
// by a `?:` property) is the only way `undefined` can show up here at all.
type IsOptionalKey<T, K extends keyof T> = undefined extends T[K] ? true : false

export type _AssertRequiredColumnIsMandatory = Expect<
  IsOptionalKey<RoomTypeWriteShape, "name"> extends false ? true : false
>
export type _AssertNonRequiredColumnIsOptional = Expect<IsOptionalKey<RoomTypeWriteShape, "id">>

// The same required column, reached through a select map that renames it — BackingColumnOf (shapes.ts) resolves
// each entry's own VALUE ("name") back to the physical column RequiredColumns names, independent of the KEY
// ("renamed_name") it's filed under, so the rename doesn't accidentally demote it to optional.
const roomTypeWriteAliased = relation("hotel.room_types", {
  select: { renamed_name: "name", base_price: "base_price" },
})

// [number] : same root-array unwrap as PropertyReadShape/PropertyWriteShape above.
export type RoomTypeWriteAliasedShape = Parameters<typeof roomTypeWriteAliased.write>[0][number]

export type _AssertAliasedRequiredColumnStaysMandatory = Expect<
  IsOptionalKey<RoomTypeWriteAliasedShape, "renamed_name"> extends false ? true : false
>

// Wellknowns (specs/typescript.md ## Wellknowns) : wellknown()'s params are supplied upfront, and its Shape/
// WriteShape come from the raw query literal via ShapeFromRelationQuery/ResolveModel (schema.example.ts) — no
// `.get(params)` argument needed, unlike relation()/func()'s own `$param`-driven Params.
const starRatedProperties = wellknown("properties_by_star_rating", { min_rating: 4 })

// Root cardinality (shapes.ts ## Root cardinality) : a well-known query is root-array-wrapped exactly like
// relation()/func() — RootShapeFromLiteralQuery (schema.example.ts's Wellknowns entry) is what wraps it.
export type StarRatedPropertiesShape = Awaited<ReturnType<typeof starRatedProperties.get>>

export type _AssertWellknownShapeIsArray = Expect<
  StarRatedPropertiesShape extends readonly unknown[] ? true : false
>
export type _AssertWellknownShapeHasStarRating = Expect<
  HasKey<StarRatedPropertiesShape[number], "star_rating">
>

// call() (querier.ts) calls .get() itself, unlike relation()/func() — unlike the rest of this file, its calls
// below would hit a real fetch() if actually run. Boxed in a never-invoked function so `just check` still
// type-checks every call, without this file's own import breaking its "never calls .get()/.write()" invariant.
function _neverRun_callChecks() {
  // Both argument forms — positional tuple and named object — against the same set-returning function.
  void call("hotel.rooms_available", [1])
  void call("hotel.rooms_available", { property_id: 1 })

  // hotel.property_average_rating is overloaded (schema.example.ts) : a scalar variant taking a whole
  // `Table__Hotel__Properties` row, and a set-returning variant taking a bare `property_id`. call()'s
  // MatchOverload (shapes.ts) must resolve each call to its OWN overload's return type, not a union of both.
  const avgByRow = call("hotel.property_average_rating", {
    property: {
      id: 1,
      chain_id: null,
      name: "x",
      location: { x: 0, y: 0 },
      star_rating: null,
      description: null,
      created_at: null,
    },
  })
  const avgById = call("hotel.property_average_rating", { property_id: 1 })

  // Zero-argument function : call()'s Args must accept `{}` (EmptyObject, schema.example.ts) directly.
  void call("hotel.property_count", {})

  // FunctionArgs (shapes.ts) rejects an argument object that doesn't match the function's own declared
  // `positional_args`/`args` — a wrong key name, a missing required one, or an extra key on a zero-argument
  // function. Each `@ts-expect-error` both proves the rejection AND fails `just check` on its own if the
  // rejection is ever silently lost (an unused directive is itself a compile error).
  // @ts-expect-error wrong argument name : "propertyId" isn't "property_id"
  void call("hotel.rooms_available", { propertyId: 1 })
  // @ts-expect-error missing required argument : neither overload's args is satisfied by `{}`
  void call("hotel.property_average_rating", {})
  // @ts-expect-error extra key on a zero-argument function's args (EmptyObject)
  void call("hotel.property_count", { foo: 1 })

  return { avgByRow, avgById }
}

type CallChecks = ReturnType<typeof _neverRun_callChecks>

export type _AssertCallMatchesScalarOverload = Expect<
  Awaited<CallChecks["avgByRow"]> extends number ? true : false
>
export type _AssertCallMatchesSetReturningOverload = Expect<
  Awaited<CallChecks["avgById"]> extends readonly unknown[] ? true : false
>

// specs/typescript-proto.md : `proto` attaches behaviour to a node's rows. `this` inside its getters/methods
// must see this node's own row shape (`room_number`, an "own" column) AND, for the parent below, its nested
// join's shape (`rooms`) too — the exact combination shapes.ts's WithProto (`ThisType`) doc comment covers.
const roomWithProto = relation("hotel.rooms", {
  proto: {
    get label() {
      return `${this.room_number} !`
    },
  },
})

export type RoomWithProtoShape = Awaited<ReturnType<typeof roomWithProto.get>>

export type _AssertProtoRowHasOwnColumn = Expect<HasKey<RoomWithProtoShape[number], "room_number">>
export type _AssertProtoRowHasProtoMember = Expect<HasKey<RoomWithProtoShape[number], "label">>

// A join's own `proto` (on "rooms" below) merges into ITS row shape independently of the parent's — exercised via
// join()'s own request literal, not a bare relation() passed straight into `join:` (query.ts's `on` is mandatory
// on a joined relation ; only join()/ScopedJoin supply it).
const propertyWithNestedProto = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms<;id:property_id", {
      proto: {
        get label() {
          return `${this.room_number} !`
        },
      },
    }),
  },
  proto: {
    get roomCount() {
      return this.rooms.length
    },
  },
})

export type PropertyWithNestedProtoShape = Awaited<ReturnType<typeof propertyWithNestedProto.get>>

export type _AssertParentProtoSeesJoinedProtoMember = Expect<
  HasKey<PropertyWithNestedProtoShape[number]["rooms"][number], "label">
>

// specs/typescript-proto.md ## Reusing a Querier in a join : `roomWithProto` (a standalone relation() Querier,
// `proto` and all) reused as a join's own `request`, instead of rewriting its `proto` inline.
const propertyWithReusedProto = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms<;id:property_id", roomWithProto),
  },
})

export type PropertyWithReusedProtoShape = Awaited<ReturnType<typeof propertyWithReusedProto.get>>

export type _AssertReusedProtoMemberIsVisible = Expect<
  HasKey<PropertyWithReusedProtoShape[number]["rooms"][number], "label">
>

// Same two cases as propertyWithNestedProto/propertyWithReusedProto above, but through the scoped-join callback
// form (specs/typescript-better-join.md) rather than a plain `join:` object literal — ScopedJoin's own callback
// overload return type must ALSO be `WithProto`, not a bare `Q`, or a `proto` nested inside what the callback
// returns silently loses `this`'s typing the same way a union parameter type would (ScopedJoin's own doc comment).
const propertyWithNestedProtoScoped = relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms<;id:property_id", {
      proto: {
        get label() {
          return `${this.room_number} !`
        },
      },
    }),
  },
  proto: {
    get roomCount() {
      return this.rooms.length
    },
  },
}))

export type PropertyWithNestedProtoScopedShape = Awaited<
  ReturnType<typeof propertyWithNestedProtoScoped.get>
>

export type _AssertScopedParentProtoSeesJoinedProtoMember = Expect<
  HasKey<PropertyWithNestedProtoScopedShape[number]["rooms"][number], "label">
>

const propertyWithReusedProtoScoped = relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms<;id:property_id", roomWithProto),
  },
}))

export type PropertyWithReusedProtoScopedShape = Awaited<
  ReturnType<typeof propertyWithReusedProtoScoped.get>
>

export type _AssertScopedReusedProtoMemberIsVisible = Expect<
  HasKey<PropertyWithReusedProtoScopedShape[number]["rooms"][number], "label">
>
