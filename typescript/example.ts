// Compile-only fixture exercising relation()/join()/func() against the "hotel" example schema
// (schema.example.ts). Until a proper runtime test harness exists, this file's
// only job is to fail `just check` if the generic machinery in querier.ts/shapes.ts stops accepting a
// well-formed query, or silently loosens some field's inferred type. Never calls .get()/.write() — those hit a
// real `fetch()` — so this file is safe to simply import, not just type-check.

import { call, func, join, relation, wellknown } from "./querier"
import type { ShapeOf, WriteShapeOf } from "./querier"
import type { Functions } from "./schema.example"
import type {
  DefaultRow,
  ParsedShortcut,
  ResolveModel,
  RootShapeFromFunctionMember,
} from "./shapes"
import type { Id, Int8, PgNumeric, PgPoint } from "./pg_values"
import {
  asNumeric,
  bytesAccessor,
  moneyAccessor,
  pointAccessor,
  timestampTzAccessor,
} from "./pg_values"

// Ids are table-local (pg_values.ts's `Id`) : a bare literal isn't one, so a literal id is cast explicitly.
const PROPERTY_1 = 1 as Id["hotel.properties.id"]
const ROOM_TYPE_1 = 1 as Id["hotel.room_types.id"]

const properties = relation("hotel.properties", {
  select: {
    col: ["col", "created_at"],
    test: ["$param", "toto", "string"],
  },
  join: {
    // root is "hotel.properties", so the key names *this* side of the relationship, not the target :
    // Relationships["hotel.properties"] is the properties -> rooms (one-to-many) entry — only one variant, so
    // there's only one valid shortcut to pick here.
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", {
      select: ["*"],
    }),
  },
})

// Exported so a change to ShapeFromQuery/ResolveModel that silently loosens/narrows this query's inferred shape
// shows up as a diff here, not just a passing compile.
export type PropertiesShape = Awaited<ReturnType<typeof properties.get>>

// docs/content/typescript/index.md ## Building a query : ShapeOf/WriteShapeOf pull Shape/WriteShape straight off
// a Querier's own type, an alternative to `Awaited<ReturnType<typeof q.get>>` above — asserted structurally
// identical to it here, not just "compiles", since the two are meant to be interchangeable.
export type PropertiesShapeOf = ShapeOf<typeof properties>
export type PropertiesWriteShapeOf = WriteShapeOf<typeof properties>

export type _AssertShapeOfMatchesGetReturn = Expect<
  [PropertiesShapeOf] extends [PropertiesShape]
    ? [PropertiesShape] extends [PropertiesShapeOf]
      ? true
      : false
    : false
>

const rooms = relation("hotel.rooms", {
  join: {
    // "hotel.rooms" has TWO FKs (properties, room_types) — Relationships["hotel.rooms"] is a union, so `join()`
    // needs the explicit shortcut to pick between them ; this is exactly the case ## Schema interfaces' old
    // `> Question:` block was about.
    property: join("hotel.rooms", "hotel.properties>id:property_id", {
      select: ["*~"],
    }),
    room_type: join("hotel.rooms", "hotel.room_types>id:room_type_id", {
      select: ["*~"],
    }),
  },
})

export type RoomsShape = Awaited<ReturnType<typeof rooms.get>>

// Scoped-join callback form : equivalent to `properties` above, but the
// `join` handed to the callback is already scoped to "hotel.properties", so nested join() calls never repeat it.
// Nested two levels deep (properties -> rooms -> room_type) to exercise TargetRelationName's recursive scoping.
const propertiesScoped = relation("hotel.properties", (join) => ({
  select: {
    col: ["col", "created_at"],
    test: ["$param", "toto", "string"],
  },
  join: {
    rooms: join("hotel.rooms*id:property_id", (join) => ({
      select: ["*"],
      join: {
        room_type: join("hotel.room_types>id:room_type_id"),
      },
    })),
  },
}))

export type PropertiesScopedShape = Awaited<ReturnType<typeof propertiesScoped.get>>

// No second argument at all : a bare select-all, per resolveRequest()'s default (querier.ts).
const propertiesBare = relation("hotel.properties")

export type PropertiesBareShape = Awaited<ReturnType<typeof propertiesBare.get>>

const roomsAvailable = func("hotel.rooms_available", {
  select: ["*"],
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
  select: ["*"],
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
    id: ["col", "id"],
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
  PropertyCountShape extends Int8 ? true : false
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
  Extract<PropertyAverageRatingResolved, PgNumeric> extends never ? false : true
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
    id: ["col", "id"],
    avg_bare: ["call", "property_average_rating", [".", "id"]],
    avg_qualified: ["call", { schema: "hotel", name: "property_average_rating" }, [".", "id"]],
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
  Extract<PropertyWithComputedShape["avg_bare"], PgNumeric> extends never ? false : true
>
export type _AssertCallQualifiedResolvesFunctionReturns = Expect<
  Extract<PropertyWithComputedShape["avg_qualified"], PgNumeric> extends never ? false : true
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

// Required fields : RequiredColumns["hotel.room_types"] names "name"/"base_price" —
// "id" is neither nullable nor required (a serial/identity column in a real deployment), so it must be OPTIONAL
// in the write shape rather than mandatory the way tsgen used to render every physical column, verbatim.
const roomTypeWrite = relation("hotel.room_types", {
  select: ["*~"],
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
  select: { renamed_name: ["col", "name"], base_price: ["col", "base_price"] },
})

// [number] : same root-array unwrap as PropertyReadShape/PropertyWriteShape above.
export type RoomTypeWriteAliasedShape = Parameters<typeof roomTypeWriteAliased.write>[0][number]

export type _AssertAliasedRequiredColumnStaysMandatory = Expect<
  IsOptionalKey<RoomTypeWriteAliasedShape, "renamed_name"> extends false ? true : false
>

// Wellknowns : wellknown()'s params are supplied upfront, and its Shape/
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
      id: PROPERTY_1,
      chain_id: null,
      name: "x",
      location: "(0,0)" as PgPoint,
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
  Awaited<CallChecks["avgByRow"]> extends PgNumeric ? true : false
>
export type _AssertCallMatchesSetReturningOverload = Expect<
  Awaited<CallChecks["avgById"]> extends readonly unknown[] ? true : false
>

// docs/content/typescript/index.md ## Attaching behavior to rows : `proto` attaches behaviour to a node's rows.
// `this` inside its getters/methods must see this node's own row shape (`room_number`, an "own" column) AND, for
// the parent below, its nested join's shape (`rooms`) too — the exact combination shapes.ts's WithProto
// (`ThisType`) doc comment covers.
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

// docs/content/typescript/index.md ## Typed wire values ### Reading a richer value : an accessor helper's descriptor-map entry composes into
// `proto` via ordinary object spread, and its `get`/`set` type against the accessor's own `this` (not `ThisType`).
const propertyWithAccessor = relation("hotel.properties", {
  proto: {
    ...timestampTzAccessor("created_at"),
    ...pointAccessor("location"),
  },
})

export type PropertyWithAccessorShape = Awaited<ReturnType<typeof propertyWithAccessor.get>>

export type _AssertAccessorProducesTemporalInstant = Expect<
  PropertyWithAccessorShape[number]["created_at_as_date"] extends Temporal.Instant ? true : false
>

// A get-only accessor entry (pointAccessor has no setter) must stay readonly in the merged shape.
function assertPointAccessorIsReadonly(row: PropertyWithAccessorShape[number]) {
  // @ts-expect-error location_as_point is get-only, assigning it is a compile error
  row.location_as_point = { x: 0, y: 0 }
}
void assertPointAccessorIsReadonly

// shapes.ts's InitShapeFromQuery doc comment : a `proto` member is never part of the write shape — it isn't a
// column, and a key in an object literal would shadow the accessor — but init() returns the row with them.
export type PropertyWithAccessorWriteShape = Parameters<
  typeof propertyWithAccessor.write
>[0][number]

export type _AssertWriteShapeHasNoAccessor = Expect<
  HasKey<PropertyWithAccessorWriteShape, "created_at_as_date"> extends false ? true : false
>
export type _AssertWriteShapeHasNoGetOnlyAccessor = Expect<
  HasKey<PropertyWithAccessorWriteShape, "location_as_point"> extends false ? true : false
>

function assertInitRowAccessors() {
  const row = propertyWithAccessor.init({ name: "x" })
  row.created_at_as_date = Temporal.Now.instant() // get+set : assignable
  // @ts-expect-error location_as_point is get-only, assigning it is a compile error on init()'s row too
  row.location_as_point = { x: 0, y: 0 }
  // an init() row is still accepted by write()
  void propertyWithAccessor.write([row])
}
void assertInitRowAccessors

// moneyAccessor/bytesAccessor (## Reusable helpers, remaining work) — both get+set, same pattern as
// timestampTzAccessor.
const roomTypeWithAccessor = relation("hotel.room_types", {
  proto: {
    ...moneyAccessor("deposit"),
    ...bytesAccessor("photo"),
  },
})

export type RoomTypeWithAccessorShape = Awaited<ReturnType<typeof roomTypeWithAccessor.get>>

export type _AssertMoneyAccessorProducesBigint = Expect<
  RoomTypeWithAccessorShape[number]["deposit_as_cents"] extends bigint ? true : false
>
export type _AssertBytesAccessorProducesUint8Array = Expect<
  RoomTypeWithAccessorShape[number]["photo_as_bytes"] extends Uint8Array ? true : false
>

function assertMoneyAndBytesAccessorsAreWritable(row: RoomTypeWithAccessorShape[number]) {
  row.deposit_as_cents = 12345n
  row.photo_as_bytes = new Uint8Array([1, 2, 3])
}
void assertMoneyAndBytesAccessorsAreWritable

// A `proto` mixing a `get` and a plain method needs an explicit return type on EVERY member (WithProto's own doc
// comment, shapes.ts) — without one, `this` silently degrades to `any` throughout the object instead of failing
// loudly, so this only guards the annotated form actually compiles with a correctly-typed `this`.
const roomWithMixedProto = relation("hotel.rooms", {
  proto: {
    get label(): string {
      return `${this.room_number} !`
    },
    describe(): string {
      return `Room ${this.room_number}, ${this.features.length} feature(s)`
    },
  },
})

export type RoomWithMixedProtoShape = Awaited<ReturnType<typeof roomWithMixedProto.get>>

export type _AssertMixedProtoHasGetter = Expect<HasKey<RoomWithMixedProtoShape[number], "label">>
export type _AssertMixedProtoHasMethod = Expect<HasKey<RoomWithMixedProtoShape[number], "describe">>

// A join's own `proto` (on "rooms" below) merges into ITS row shape independently of the parent's — exercised via
// join()'s own request literal, not a bare relation() passed straight into `join:` (query.ts's `on` is mandatory
// on a joined relation ; only join()/ScopedJoin supply it).
const propertyWithNestedProto = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", {
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

// docs/content/typescript/index.md ## Building a query : `roomWithProto` (a standalone relation() Querier,
// `proto` and all) reused as a join's own `request`, instead of rewriting its `proto` inline.
const propertyWithReusedProto = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", roomWithProto),
  },
})

export type PropertyWithReusedProtoShape = Awaited<ReturnType<typeof propertyWithReusedProto.get>>

export type _AssertReusedProtoMemberIsVisible = Expect<
  HasKey<PropertyWithReusedProtoShape[number]["rooms"][number], "label">
>

// Same two cases as propertyWithNestedProto/propertyWithReusedProto above, but through the scoped-join callback
// form rather than a plain `join:` object literal — ScopedJoin's own callback
// overload return type must ALSO be `WithProto`, not a bare `Q`, or a `proto` nested inside what the callback
// returns silently loses `this`'s typing the same way a union parameter type would (ScopedJoin's own doc comment).
const propertyWithNestedProtoScoped = relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms*id:property_id", {
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
    rooms: join("hotel.rooms*id:property_id", roomWithProto),
  },
}))

export type PropertyWithReusedProtoScopedShape = Awaited<
  ReturnType<typeof propertyWithReusedProtoScoped.get>
>

export type _AssertScopedReusedProtoMemberIsVisible = Expect<
  HasKey<PropertyWithReusedProtoScopedShape[number]["rooms"][number], "label">
>

// docs/content/typescript/index.md ## Attaching behavior to rows : a get+set accessor entry must be
// writable in the merged shape, not `readonly` — assigning it directly is the test.
function assertAccessorIsWritable(row: PropertyWithAccessorShape[number]) {
  row.created_at_as_date = Temporal.Now.instant()
}
void assertAccessorIsWritable

// docs/content/typescript/index.md ### Building a new row : `rooms` joins "property"/"room_type" outgoing (">", to-one) —
// init()'s own `data` parameter must supply both as a single nested row, not `[]`, and that nested row's own
// shape must already match its relation's WriteShape (own columns only, per `select: ["*~"]` on both joins).
const createdRoom = rooms.init({
  property_id: PROPERTY_1,
  room_type_id: ROOM_TYPE_1,
  room_number: "101",
  property: { name: "Marina Bay Grand Hotel" },
  room_type: { name: "Deluxe", base_price: asNumeric("129.99") },
})

export type _AssertCreateNestedToOneIsObject = Expect<
  typeof createdRoom.property extends readonly unknown[] ? false : true
>
export type _AssertCreateNestedToOneHasOwnColumn = Expect<HasKey<typeof createdRoom.property, "id">>

// `propertyWithNestedProto` joins "rooms" incoming-and-multiple ("*", to-many) — init()'s own `data` parameter
// must supply it as an array. (Unlike `properties`, its select defaults to "*" rather than an expression map
// that omits "rooms".)
const createdProperty = propertyWithNestedProto.init({
  name: "Marina Bay Grand Hotel",
  rooms: [{ property_id: PROPERTY_1, room_type_id: ROOM_TYPE_1, room_number: "101" }],
})

export type _AssertCreateNestedToManyIsArray = Expect<
  typeof createdProperty.rooms extends readonly unknown[] ? true : false
>

// init()'s own returned row carries the same `proto` accessors a read row does, writable the same way.
function assertCreatedRowAccessorIsWritable() {
  const createdPropertyWithAccessor = propertyWithAccessor.init({
    name: "Marina Bay Grand Hotel",
  })
  createdPropertyWithAccessor.created_at_as_date = Temporal.Now.instant()
}
void assertCreatedRowAccessorIsWritable

// relation()'s `const Q extends RelationQuery<...>` signature infers Q's literal type from `request` ; a
// PRE-DECLARED `as const` value (unlike an inline literal, which is checked against RelationQuery's own field
// types via contextual typing before the `const` modifier's readonly-inference applies) keeps its `readonly`
// array/tuple type once inferred as Q. Every array/tuple position on RelationQuery/Expression (query.ts) is
// `readonly` specifically so this still typechecks, not just the inline-literal form above.
const propertyInsertColumns = ["name", "star_rating"] as const
const propertyWhere = ["=", ["col", "star_rating"], 5] as const
const propertiesWithReusedConstants = relation("hotel.properties", {
  insert_columns: propertyInsertColumns,
  where: propertyWhere,
})
void propertiesWithReusedConstants

// "."/"dot" (shapes.ts's ShapeFromDotTag) : the general "look this up in scope" tag docs/content/query-language/
// selecting.md ## The dot-chain form describes — pins that a real column, a joined relation (by its `join` key,
// not the target relation's name), and a bare-name computed field all resolve through it, and that a 2+-hop
// chain drills into a joined relation's own field. Pre-fix, "."/"dot" fell straight through ShapeFromExpression's
// dispatcher to the generic `unknown` catch-all — none of these ever resolved.
const roomsWithDot = relation("hotel.rooms", (join) => ({
  select: {
    property_id: [".", "property_id"],
    room_type_name: [".", [".", "room_type"], "name"],
    // Flat chain form (query/expression_parse.go's "." case, shapes.ts's ShapeFromDotTag) : a bare name in the
    // leading operand position is always a scope lookup, so this must resolve identically to the nested form
    // above — no need to wrap "room_type" in its own [".", ...] just to get it treated as a lookup.
    room_type_name_flat: [".", "room_type", "name"],
  },
  join: {
    room_type: join("hotel.room_types>id:room_type_id"),
  },
}))
export type RoomsWithDotShape = Awaited<ReturnType<typeof roomsWithDot.get>>[number]

export type _AssertDotColumnResolvesRealType = Expect<
  RoomsWithDotShape["property_id"] extends number ? true : false
>
export type _AssertDotChainIntoJoinResolvesRealType = Expect<
  RoomsWithDotShape["room_type_name"] extends string ? true : false
>
export type _AssertDotChainFlatFormMatchesNestedForm = Expect<
  RoomsWithDotShape["room_type_name_flat"] extends string ? true : false
>

const propertyWithDotJoinAndComputed = relation("hotel.properties", (join) => ({
  select: {
    id: [".", "id"],
    rooms: [".", "rooms"],
    avg_rating: [".", "property_average_rating"],
  },
  join: {
    rooms: join("hotel.rooms*id:property_id"),
  },
}))
export type PropertyWithDotJoinShape = Awaited<
  ReturnType<typeof propertyWithDotJoinAndComputed.get>
>[number]

// "rooms" is reached through a "*" (incoming-and-multiple) shortcut, so a bare "." single-hop lookup must stay
// array-shaped, same as JoinShapes already gets right for the default "*" select — ShapeFromDotSingleHop reuses
// the same JoinMemberShape cardinality logic rather than a separate, potentially-drifting copy.
export type _AssertDotJoinLookupIsArray = Expect<
  PropertyWithDotJoinShape["rooms"] extends readonly unknown[] ? true : false
>
export type _AssertDotJoinLookupHasOwnColumn = Expect<
  HasKey<PropertyWithDotJoinShape["rooms"][number], "room_number">
>
export type _AssertDotBareNameResolvesComputedField = Expect<
  Extract<PropertyWithDotJoinShape["avg_rating"], PgNumeric> extends never ? false : true
>
void roomsWithDot
void propertyWithDotJoinAndComputed

// Write side mirrors the read side (shapes.ts's WriteShapeFromDotTag) : same ShapeFromExpression-dispatcher gap,
// so pre-fix a `.`-selected column/join fell through to `unknown` in init()/write() too, even though a
// single-hop `.` reference to a real column is just as writable as `col`/`set` (server/query/shape.go's
// walkSelectForWritability records a plain *Identifier the same way) — this is the "init() types less strongly
// than get()" regression, not a display-only issue like the hover one above.
export type RoomsWithDotWriteShape = Parameters<typeof roomsWithDot.write>[0][number]
export type _AssertDotColumnIsRequiredInWriteShape = Expect<
  HasKey<RoomsWithDotWriteShape, "property_id">
>
// A multi-hop chain never backs a single real column (BackingColumnOf), and per docs/content/query-language/
// writing.md ## Writability rules a computed/derived value is never a write target either way — dropped
// entirely, same as `get`/`call`, rather than kept with a nonsensical type.
export type _AssertDotChainOmittedFromWriteShape = Expect<
  HasKey<RoomsWithDotWriteShape, "room_type_name"> extends false ? true : false
>

export type PropertyWithDotJoinWriteShape = Parameters<
  typeof propertyWithDotJoinAndComputed.write
>[0][number]
// A single-hop `.` reference to a joined relation recurses into that relation's own write shape, same as a
// "*"-selected join already does (WriteJoinMemberShape, shared with WriteJoinShapes).
export type _AssertDotJoinWritableInWriteShape = Expect<
  HasKey<PropertyWithDotJoinWriteShape, "rooms">
>
// A joined relation is never optional in the write shape — WriteJoinShapes (default "*") never wraps it in
// Partial, and an explicit object-literal select map (IsRequiredEntry's own BackingJoinOf branch) must match
// that, not fall through to "no backing column, so optional" the way a genuinely optional column does.
// `{} extends Pick<T, K>` is the standard required-vs-optional probe : true iff K could be entirely absent.
type IsRequiredKey<T, K extends keyof T> = Record<never, never> extends Pick<T, K> ? false : true
export type _AssertDotJoinIsRequiredInWriteShape = Expect<
  IsRequiredKey<PropertyWithDotJoinWriteShape, "rooms">
>
// A bare-name computed field is never a write target (writing.md ## Writability rules) — dropped, same as the
// multi-hop chain above.
export type _AssertDotComputedFieldOmittedFromWriteShape = Expect<
  HasKey<PropertyWithDotJoinWriteShape, "avg_rating"> extends false ? true : false
>

// Join nullability (shapes.ts's IsNullableJoin) : "*" (incoming-and-multiple) is never null — an empty array,
// not an absent one ; "<" (incoming-and-unique) is unconditionally nullable — nothing on THIS relation's own
// row guarantees a matching row exists on the other side ; ">" (outgoing) is nullable iff at least one of its
// own FK columns (named in the shortcut's own pairs) is itself nullable on the declaring relation.
// schema.example.ts's Table__Hotel__PropertySettings/Relationships["hotel.properties"] doc comments cover why
// each fixture relationship exists.
const propertyWithAllJoinKinds = relation("hotel.properties", (join) => ({
  join: {
    // "*" : rooms.property_id -> properties.id, incoming-and-multiple.
    rooms: join("hotel.rooms*id:property_id"),
    // ">" : properties.chain_id -> properties.id, chain_id IS nullable.
    chain: join("hotel.properties>id:chain_id"),
    // "<" : property_settings.property_id -> properties.id, incoming-and-unique.
    settings: join("hotel.property_settings<property_id:id"),
  },
}))
export type PropertyWithAllJoinKindsShape = Awaited<
  ReturnType<typeof propertyWithAllJoinKinds.get>
>[number]

export type _AssertToManyJoinNeverNull = Expect<
  null extends PropertyWithAllJoinKindsShape["rooms"] ? false : true
>
export type _AssertOutgoingNullableFkJoinIsNullable = Expect<
  null extends PropertyWithAllJoinKindsShape["chain"] ? true : false
>
export type _AssertIncomingUniqueJoinIsAlwaysNullable = Expect<
  null extends PropertyWithAllJoinKindsShape["settings"] ? true : false
>

// property_settings' own outgoing join back to properties (property_id, NOT NULL) contrasts with "chain" above —
// same ">" marker, non-nullable FK column this time, so no `| null`.
const propertySettingsWithOutgoingJoin = relation("hotel.property_settings", (join) => ({
  join: {
    property: join("hotel.properties>id:property_id"),
  },
}))
export type PropertySettingsWithOutgoingJoinShape = Awaited<
  ReturnType<typeof propertySettingsWithOutgoingJoin.get>
>[number]
export type _AssertOutgoingNonNullableFkJoinIsNotNullable = Expect<
  null extends PropertySettingsWithOutgoingJoinShape["property"] ? false : true
>
void propertyWithAllJoinKinds
void propertySettingsWithOutgoingJoin

// A nested object-literal select value — grouping several of this relation's own columns under one alias key,
// rather than a `col`/`set`/`.` tag naming ONE column or join — has no single BackingColumnOf/BackingJoinOf of
// its own ; IsRequiredEntry must recurse into its own write shape instead of defaulting straight to "optional"
// the way a genuinely unbacked entry (a container, `$param`, ...) does. "project" groups `name` (required, per
// RequiredColumns["hotel.properties"]) with `id` (not required) — required-ness propagates up : since ANY of
// its own keys is required, "project" itself must be present whenever writing this query.
const projectFields = {
  id: [".", "id"] as const,
  name: [".", "name"] as const,
}
const propertyWithNestedSelectGroup = relation("hotel.properties", (join) => ({
  select: {
    project: projectFields,
    rooms: [".", "rooms"],
  },
  join: {
    rooms: join("hotel.rooms*id:property_id"),
  },
}))
export type PropertyWithNestedSelectGroupWriteShape = Parameters<
  typeof propertyWithNestedSelectGroup.write
>[0][number]
export type _AssertNestedSelectGroupIsRequiredInWriteShape = Expect<
  IsRequiredKey<PropertyWithNestedSelectGroupWriteShape, "project">
>

// A nested group whose own columns are ALL optional (no required column inside) stays optional itself — the
// whole group can just as well be omitted wholesale, same as any other optional column.
const optionalFields = {
  star_rating: [".", "star_rating"] as const,
  description: [".", "description"] as const,
}
const propertyWithOptionalNestedSelectGroup = relation("hotel.properties", {
  select: {
    optional_group: optionalFields,
  },
})
export type PropertyWithOptionalNestedSelectGroupWriteShape = Parameters<
  typeof propertyWithOptionalNestedSelectGroup.write
>[0][number]
export type _AssertFullyOptionalNestedSelectGroupStaysOptional = Expect<
  IsRequiredKey<PropertyWithOptionalNestedSelectGroupWriteShape, "optional_group"> extends false
    ? true
    : false
>
void propertyWithNestedSelectGroup
void propertyWithOptionalNestedSelectGroup

// querier.ts's JoinOf<Q>/WithComputedKeys : relation()/func()/join()/ScopedJoin's own `const Q extends
// RelationQuery<Rel, ...>` previously left Join at its permissive default (`{ [name: string]: RelationQuery }`,
// keyof = bare `string`), which silently made EVERY column/alias reference in select/where accept any string at
// all — no compile error, no editor completions, regardless of Rel's real shape. JoinOf<Q> reads Join back off
// Q itself (F-bounded), closing that ; these pin both directions : a real column/alias/computed-field name still
// compiles, and a typo of one is now rejected.
// @ts-expect-error unrecognized column name via "col"
void relation("hotel.rooms", { select: { x: ["col", "totally_bogus_column"] } })
// @ts-expect-error unrecognized name via "." (not a column, not a join alias, not a computed field)
void relation("hotel.rooms", { select: { x: [".", "totally_bogus_name"] } })
// Same, one level down through a nested join — TS reports the error on the inner join() call itself.
void relation("hotel.properties", (join) => ({
  join: {
    // @ts-expect-error unrecognized column name via "col", inside a nested join
    rooms: join("hotel.rooms*id:property_id", { select: { x: ["col", "totally_bogus_nested"] } }),
  },
}))

// A bare-name computed field (schema.example.ts's ComputedProperties) is `.`-reachable exactly like a real
// column or join alias — WithComputedKeys folds it into K at the ONE place that needs it (the Q constraint),
// without leaking into "*"'s own default shape (PropertyReadShape, further above, never carries it).
const propertyWithValidComputedDot = relation("hotel.properties", {
  select: { avg: [".", "property_average_rating"] },
})
void propertyWithValidComputedDot
// @ts-expect-error typo of a real computed field name
void relation("hotel.properties", { select: { avg: [".", "property_average_ratingg"] } })

export type _AssertComputedFieldExcludedFromStarShape = Expect<
  HasKey<PropertiesBareShape[number], "property_average_rating"> extends false ? true : false
>

// A `.` chain's trailing hops (past the leading name) are intentionally UNCHECKED strings, not K : a hop past
// the first lands on whatever the PREVIOUS hop resolved to — a different relation's own keys, which
// Expression<K>'s single type parameter has no way to reference (query.ts's own doc comment on "."/"dot", above
// roomsWithDot). Only the leading operand is checked, so a nonexistent COLUMN there is still caught even though
// a nonexistent HOP past it (checked instead by shapes.ts's ShapeFromDotChain, at the read-shape level, falling
// back to `unknown` per Hop extends keyof Base) is not caught until then.
const roomsWithBogusHopButValidLeadingName = relation("hotel.rooms", (join) => ({
  select: { x: [".", "room_type", "this_hop_name_is_not_checked_against_anything"] },
  join: { room_type: join("hotel.room_types>id:room_type_id") },
}))
type RoomsWithBogusHopShape = Awaited<
  ReturnType<typeof roomsWithBogusHopButValidLeadingName.get>
>[number]
// The bogus hop compiles (unchecked), but ShapeFromDotChain still can't resolve it against room_types' own
// fields at the SHAPE level — falls back to `unknown` there instead, same as any other unresolvable hop.
export type _AssertUncheckedHopFallsBackToUnknownAtShapeLevel = Expect<
  [RoomsWithBogusHopShape["x"]] extends [string] ? false : true
>

// querier.ts's ValidatedJoin : a join can be written as a plain object literal carrying `shortcut` directly,
// with no call to join()/ScopedJoin — shortcut/select/nested-join are all checked exactly as the join()-built
// form is, since ValidatedJoin constrains Q's own REAL inferred `join` field rather than reconstructing it.
const propertyPlainJoin = relation("hotel.rooms", {
  join: {
    property: {
      shortcut: "hotel.properties>id:property_id",
      select: ["*~"],
    },
  },
})
export type PropertyPlainJoinShape = Awaited<ReturnType<typeof propertyPlainJoin.get>>
// Same row shape as the join()-built equivalent (rooms' first overload test, near the top of this file) : the
// plain-object path goes through the exact same ResolveModel/ShapeFromRelationQuery machinery.
export type _AssertPlainJoinShapeHasTargetColumns = Expect<
  HasKey<PropertyPlainJoinShape[number]["property"], "name">
>

// A mismatched overload set reports the error against the LAST overload tried (the callback one), not the plain-
// object one actually intended — TS's own behavior once no overload matches, not something ValidatedJoin
// controls — so the useful "shortcut not assignable" diagnostic lands one line down, on the object literal
// itself, rather than on the `relation(...)` call line.
void relation("hotel.rooms", {
  // @ts-expect-error "hotel.property_settings<property_id:id" isn't reachable from "hotel.rooms"
  join: { property: { shortcut: "hotel.property_settings<property_id:id" } },
})

// Nested two levels deep, same as the callback form's own nesting test above (propertiesScoped) : each level's
// `shortcut` is checked against ITS OWN target relation, not the root's.
const propertyPlainJoinNested = relation("hotel.properties", {
  join: {
    rooms: {
      shortcut: "hotel.rooms*id:property_id",
      select: { x: [".", "room_type", "name"] },
      join: {
        room_type: { shortcut: "hotel.room_types>id:room_type_id" },
      },
    },
  },
})
void propertyPlainJoinNested

// The nested shortcut is valid for "hotel.properties" -> rooms, but NOT reachable from rooms' own target
// (hotel.room_types).
void relation("hotel.properties", {
  // @ts-expect-error nested shortcut invalid one level down
  join: {
    rooms: {
      shortcut: "hotel.rooms*id:property_id",
      join: {
        room_type: { shortcut: "hotel.rooms*id:property_id" },
      },
    },
  },
})

// A plain join entry's own nested-join alias is still `.`-reachable from that entry's own select, same as the
// join()-built form (JoinOf<Q> is read off the entry's real inferred type, not rebuilt).
void relation("hotel.properties", {
  join: {
    rooms: {
      shortcut: "hotel.rooms*id:property_id",
      select: { rt: [".", "room_type"] },
      join: { room_type: { shortcut: "hotel.room_types>id:room_type_id" } },
    },
  },
})
void relation("hotel.properties", {
  // @ts-expect-error "room_typ" isn't a join alias on this entry (typo of "room_type")
  join: {
    rooms: {
      shortcut: "hotel.rooms*id:property_id",
      select: { rt: [".", "room_typ"] },
      join: { room_type: { shortcut: "hotel.room_types>id:room_type_id" } },
    },
  },
})
void roomsWithBogusHopButValidLeadingName

// ContainerTag (shapes.ts's ContainerTagShape) : "arr"/"lst" preserve each item's
// own position as a tuple ; "coalesce" collapses to the union of what each argument could be. None of these were
// previously exercised here — added as a baseline before shapes.ts's own read/write dispatchers are collapsed.
const roomWithContainers = relation("hotel.rooms", {
  select: {
    coords: ["arr", ["col", "floor"], ["col", "room_number"]],
    tags: ["lst", ["col", "status"]],
    floor_or_zero: ["coalesce", ["col", "floor"], 0],
  },
})
export type RoomWithContainersShape = Awaited<ReturnType<typeof roomWithContainers.get>>[number]

export type _AssertArrContainerIsTuple = Expect<
  RoomWithContainersShape["coords"] extends readonly [number | null, string] ? true : false
>
export type _AssertLstContainerIsTuple = Expect<
  RoomWithContainersShape["tags"] extends readonly [infer S]
    ? S extends "clean" | "dirty"
      ? true
      : false
    : false
>
export type _AssertCoalesceContainerUnionsArgs = Expect<
  RoomWithContainersShape["floor_or_zero"] extends number | null ? true : false
>

// A container-tag entry has no single BackingColumnOf/BackingJoinOf of its own (unlike a bare `col`/`.` reference)
// — IsRequiredEntry can't read requiredness through it, so it's always optional in the write shape, even when
// every item inside references a required column.
export type RoomWithContainersWriteShape = Parameters<typeof roomWithContainers.write>[0][number]
export type _AssertContainerEntryStaysOptionalInWriteShape = Expect<
  IsRequiredKey<RoomWithContainersWriteShape, "coords"> extends false ? true : false
>
export type _AssertArrContainerWritableInWriteShape = Expect<
  HasKey<RoomWithContainersWriteShape, "coords">
>

// OwnFullTag's rest args (shapes.ts's ExceptFromRest/AndFromRest) : an except-list drops columns from "*"/"*~" ;
// an and-map adds/overrides computed columns on top. Neither was previously exercised here.
const propertyWithExceptList = relation("hotel.properties", {
  select: ["*", ["star_rating", "description"]],
})
export type PropertyWithExceptListShape = Awaited<
  ReturnType<typeof propertyWithExceptList.get>
>[number]
export type _AssertExceptListDropsColumn = Expect<
  HasKey<PropertyWithExceptListShape, "star_rating"> extends false ? true : false
>
export type _AssertExceptListKeepsOtherColumns = Expect<HasKey<PropertyWithExceptListShape, "id">>

const propertyWithOwnRestArgs = relation("hotel.properties", {
  select: ["*~", ["description"], { avg_rating: ["call", "property_average_rating", [".", "id"]] }],
})
export type PropertyWithOwnRestArgsShape = Awaited<
  ReturnType<typeof propertyWithOwnRestArgs.get>
>[number]
export type _AssertAndMapAddsComputedColumn = Expect<
  Extract<PropertyWithOwnRestArgsShape["avg_rating"], PgNumeric> extends never ? false : true
>
export type _AssertExceptListAppliesBeforeAndMap = Expect<
  HasKey<PropertyWithOwnRestArgsShape, "description"> extends false ? true : false
>

// The and-map's own "call" entry is Omitted on the write side (never a real column) ; the except-list still
// drops "description" from the write shape's base, same as the read side.
export type PropertyWithOwnRestArgsWriteShape = Parameters<
  typeof propertyWithOwnRestArgs.write
>[0][number]
export type _AssertAndMapComputedColumnOmittedFromWriteShape = Expect<
  HasKey<PropertyWithOwnRestArgsWriteShape, "avg_rating"> extends false ? true : false
>
export type _AssertExceptListDropsColumnFromWriteShape = Expect<
  HasKey<PropertyWithOwnRestArgsWriteShape, "description"> extends false ? true : false
>
export type _AssertOwnRestArgsKeepsRequiredColumnMandatory = Expect<
  IsOptionalKey<PropertyWithOwnRestArgsWriteShape, "name"> extends false ? true : false
>

// ParsedShortcut (shapes.ts)'s own type-level parse and querier.ts's RUNTIME parseShortcut can't literally share
// an implementation — one parses a string's own literal TYPE at compile time, the other a string VALUE at
// request-build time — so this pins the two against each other for one fixed shortcut, the only guard against
// them silently drifting apart. `rooms`'s own "property" join (near the top of this file) is built from exactly
// this shortcut.
type _RoomsPropertyJoinParsed = ParsedShortcut<"hotel.properties>id:property_id">
export type _AssertParsedShortcutTarget = Expect<
  _RoomsPropertyJoinParsed["target"] extends "hotel.properties" ? true : false
>
export type _AssertParsedShortcutMarker = Expect<
  _RoomsPropertyJoinParsed["marker"] extends ">" ? true : false
>

function assertRuntimeParseMatchesTypeLevelParse() {
  const on = (rooms.query as unknown as { join: { property: { on: Record<string, string> } } }).join
    .property.on
  if (on.id !== "property_id") {
    throw new Error(
      `querier.ts's runtime parseShortcut produced { ${JSON.stringify(on)} } for "hotel.properties>id:property_id" — no longer matching ParsedShortcut's (shapes.ts) own type-level reading of the same shortcut.`,
    )
  }
}
// Actually invoked (unlike this file's compile-only `assertXxx` helpers) : the check only means something if it
// runs, since it's comparing two RUNTIME values, not pinning a type.
assertRuntimeParseMatchesTypeLevelParse()

// ResolveKey's shortcut branch (shapes.ts) previously only matched "<"/">" — a "*" (incoming-and-multiple)
// shortcut fell through to `never`, which made RequiredKeysOf<ResolveKey<Q>> collapse to `never` too, silently
// dropping every required-column check on a to-many join's own write shape. `propertyWithNestedProto`'s "rooms"
// join (near the top of this file) uses a "*" shortcut : "room_type_id" is required on "hotel.rooms"
// (RequiredColumns, schema.example.ts) and the join doesn't fill it, so it stays mandatory. "property_id" is the
// column the join itself fills, so it is optional there (JoinRequiredKeys, shapes.ts).
export type _AssertStarShortcutJoinRequiredColumnStaysMandatory = Expect<
  IsRequiredKey<
    Parameters<typeof propertyWithNestedProto.write>[0][number]["rooms"][number],
    "room_type_id"
  >
>
export type _AssertStarShortcutJoinKeyIsOptional = Expect<
  IsRequiredKey<
    Parameters<typeof propertyWithNestedProto.write>[0][number]["rooms"][number],
    "property_id"
  > extends false
    ? true
    : false
>

// docs/content/typescript/index.md ### Building a new row : init() keeps what `data` carried. `star_rating` is
// optional in the write shape ; passing it makes it required on the returned row, leaving it out doesn't.
const initBare = propertyWithNestedProto.init({ name: "Marina Bay Grand Hotel", rooms: [] })
const initWithRating = propertyWithNestedProto.init({
  name: "Marina Bay Grand Hotel",
  star_rating: 5,
  rooms: [],
})

type OptionalKey<T, K extends keyof T> = Record<never, never> extends Pick<T, K> ? true : false

export type _AssertInitOmittedStaysOptional = Expect<OptionalKey<typeof initBare, "star_rating">>
export type _AssertInitProvidedBecomesRequired = Expect<
  OptionalKey<typeof initWithRating, "star_rating"> extends false ? true : false
>
// Declared column type, not the literal : the row stays assignable afterwards.
export type _AssertInitProvidedKeepsColumnType = Expect<
  [typeof initWithRating.star_rating] extends [number | null]
    ? [number | null] extends [typeof initWithRating.star_rating]
      ? true
      : false
    : false
>
// An explicit `undefined` is not "provided".
const initUndefined = propertyWithNestedProto.init({ name: "x", star_rating: undefined, rooms: [] })
export type _AssertInitUndefinedStaysOptional = Expect<
  OptionalKey<typeof initUndefined, "star_rating">
>

// Still accepted by write(), the minimum viable shape.
void propertyWithNestedProto.write([initWithRating])

// Joined rows get the same treatment, in a to-many array and in a to-one object.
const initNestedMany = propertyWithNestedProto.init({
  name: "x",
  rooms: [{ property_id: PROPERTY_1, room_type_id: ROOM_TYPE_1, room_number: "101", floor: 2 }],
})
export type _AssertInitNestedManyProvidedBecomesRequired = Expect<
  OptionalKey<(typeof initNestedMany.rooms)[number], "floor"> extends false ? true : false
>
const initNestedOne = rooms.init({
  property_id: PROPERTY_1,
  room_type_id: ROOM_TYPE_1,
  room_number: "101",
  property: { name: "x", star_rating: 3 },
  room_type: { name: "Deluxe", base_price: asNumeric("129.99") },
})
export type _AssertInitNestedOneProvidedBecomesRequired = Expect<
  OptionalKey<typeof initNestedOne.property, "star_rating"> extends false ? true : false
>

// An incoming join fills the joined row's referencing column itself (writing.md ## Write order), so a nested
// `rooms` row may omit `property_id` ; the same relation written on its own still requires it. Columns the join
// doesn't fill stay required.
const initNestedOmitsJoinKey = propertyWithNestedProto.init({
  name: "x",
  rooms: [{ room_type_id: ROOM_TYPE_1, room_number: "101" }],
})
void initNestedOmitsJoinKey
function assertJoinKeyOnlyOptionalWhenJoinFillsIt() {
  propertyWithNestedProto.init({
    name: "x",
    // @ts-expect-error room_type_id isn't filled by the join
    rooms: [{ room_number: "101" }],
  })
  // @ts-expect-error property_id is required when rooms is written as the root
  rooms.init({
    room_type_id: ROOM_TYPE_1,
    room_number: "101",
    property: { name: "x" },
    room_type: { name: "d", base_price: asNumeric("1") },
  })
}
void assertJoinKeyOnlyOptionalWhenJoinFillsIt

// A proto accessor key is not accepted in `data` : it would shadow the accessor with a plain property.
function assertInitRejectsAccessorKey() {
  propertyWithAccessor.init({
    name: "x",
    // @ts-expect-error created_at_as_date is a proto member, not a column
    created_at_as_date: Temporal.Now.instant(),
  })
}
void assertInitRejectsAccessorKey

// Bogus fields are rejected, at the root and inside joins. Each directive sits right above the offending key.
function assertInitRejectsBogusFields() {
  propertyWithNestedProto.init({
    name: "x",
    // @ts-expect-error `nmae` isn't a column
    nmae: "typo",
    rooms: [],
  })
  propertyWithNestedProto.init({
    name: "x",
    rooms: [
      {
        property_id: PROPERTY_1,
        room_type_id: ROOM_TYPE_1,
        room_number: "1",
        // @ts-expect-error `bogus` isn't a column of the joined row
        bogus: 1,
      },
    ],
  })
  rooms.init({
    property_id: PROPERTY_1,
    room_type_id: ROOM_TYPE_1,
    room_number: "1",
    // @ts-expect-error `bogus` isn't a column of the to-one joined row
    property: { name: "x", bogus: 1 },
    room_type: { name: "D", base_price: asNumeric("1") },
  })
}
void assertInitRejectsBogusFields

// pg_values.ts's `Id` : table-local ids (docs/content/typescript/index.md ### Table-local id types).
type PropertyId = Id["hotel.properties.id"]
type RoomTypeId = Id["hotel.room_types.id"]

// Assignable to the bare int type, never the other way round, never to another table's id.
export type _AssertIdWidensToBareNumber = Expect<PropertyId extends number ? true : false>
export type _AssertBareNumberIsNotAnId = Expect<number extends PropertyId ? false : true>
export type _AssertIdsOfDifferentTablesDiffer = Expect<PropertyId extends RoomTypeId ? false : true>

// Same for a bigserial : the id is an Int8 (a string on the wire) plus the table tag. Spelled the way
// tsgen/ids.go generates it, since the fixture schema has no bigserial.
type LedgerId = Int8 & { readonly __id: "hotel.ledgers.id" }
export type _AssertBigIdWidensToBareInt8 = Expect<LedgerId extends Int8 ? true : false>
export type _AssertBareInt8IsNotAnId = Expect<Int8 extends LedgerId ? false : true>
export type _AssertBigIdIsNotAnotherTablesId = Expect<LedgerId extends PropertyId ? false : true>

// A foreign key column carries the id type of the column it references, in reads and in writes.
type RoomReadShape = Awaited<ReturnType<typeof rooms.get>>[number]
export type _AssertForeignKeyFollowsReferencedId = Expect<
  RoomReadShape["property_id"] extends PropertyId ? true : false
>
export type _AssertForeignKeyIsNotAnotherTablesId = Expect<
  RoomReadShape["property_id"] extends RoomTypeId ? false : true
>

function assertIdMixups() {
  const propertyId: PropertyId = PROPERTY_1
  const asNumber: number = propertyId
  void asNumber
  // @ts-expect-error a bare number isn't an id
  const fromNumber: PropertyId = 1 as number
  void fromNumber
  // @ts-expect-error another table's id isn't a properties id
  const wrongTable: PropertyId = ROOM_TYPE_1
  void wrongTable
  // @ts-expect-error a cast can't launder one table's id into another's either
  void (PROPERTY_1 as RoomTypeId)
  const bigId = "5" as LedgerId
  const asInt8: Int8 = bigId
  void asInt8
}
void assertIdMixups

// A rejected value must not make init() forget the other keys : only the offending key is reported, and the
// returned row still has `star_rating` required (it was supplied).
const initWithBadValue = propertyWithNestedProto.init({
  // @ts-expect-error name is a string
  name: 42,
  star_rating: 5,
  rooms: [],
})
export type _AssertInitKeepsProvidedKeysDespiteBadValue = Expect<
  OptionalKey<typeof initWithBadValue, "star_rating"> extends false ? true : false
>
