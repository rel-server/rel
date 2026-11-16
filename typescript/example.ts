// Compile-only fixture against the "hotel" example schema : fails `just check` if querier.ts/shapes.ts stop accepting
// a well-formed query or loosen an inferred type. Never calls .get()/.write() (they fetch), so it is safe to import.

import { call, func, join, relation, wellknown } from "./querier"
import type { Functions } from "./schema.example"
import type {
  DefaultRow,
  ParsedShortcut,
  ResolveModel,
  RootShapeFromFunctionMember,
} from "./shapes"
import type { Id, Int8, PgNumeric, PgPoint } from "./pg_values"
import { asNumeric, bytesAccessor, moneyAccessor, pointAccessor } from "./pg_values"
// The `temporal` codec's accessor.
import { timestampTzAccessor } from "./codecs/time-temporal"

// Ids are table-local : a literal id is cast explicitly.
const PROPERTY_1 = 1 as Id["hotel.properties.id"]
const ROOM_TYPE_1 = 1 as Id["hotel.room_types.id"]

const properties = relation("hotel.properties", {
  select: {
    col: ["col", "created_at"],
    test: ["$param", "toto", "string"],
  },
  join: {
    // The key names this side of the relationship, not the target.
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", {
      select: ["*"],
    }),
  },
})

// Exported so a change to the inferred shape shows up as a diff.
export type PropertiesShape = Awaited<ReturnType<typeof properties.get>>

// `typeof q.$row` is identical to one row of what get() returns.
export type PropertiesRow = typeof properties.$row

export type _AssertRowMatchesGetReturn = Expect<
  [PropertiesRow] extends [PropertiesShape[number]]
    ? [PropertiesShape[number]] extends [PropertiesRow]
      ? true
      : false
    : false
>

// A fetched row is assignable to a draft : `$draft` is the write shape plus `proto`, and a row has every column.
export type _AssertRowAssignableToDraft = Expect<
  [typeof properties.$row] extends [typeof properties.$draft] ? true : false
>

const rooms = relation("hotel.rooms", {
  join: {
    // "hotel.rooms" has two foreign keys : the shortcut picks one.
    property: join("hotel.rooms", "hotel.properties>id:property_id", {
      select: ["*~"],
    }),
    room_type: join("hotel.rooms", "hotel.room_types>id:room_type_id", {
      select: ["*~"],
    }),
  },
})

export type RoomsShape = Awaited<ReturnType<typeof rooms.get>>

// Scoped-join callback form of `properties`, two levels deep to check the nested join is scoped to its target.
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

// No second argument : select all.
const propertiesBare = relation("hotel.properties")

export type PropertiesBareShape = Awaited<ReturnType<typeof propertiesBare.get>>

const roomsAvailable = func("hotel.rooms_available", {
  select: ["*"],
})

export type RoomsAvailableShape = Awaited<ReturnType<typeof roomsAvailable.get>>

// func()'s `arguments` may use `$param`, which then shows in get()'s params.
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

// func() rejects `arguments` that don't match the declared ones.
// @ts-expect-error wrong argument name : "propertyId" isn't "property_id"
void func("hotel.rooms_available", { arguments: { propertyId: 1 } })
// @ts-expect-error missing required argument : neither overload's args is satisfied by `{}`
void func("hotel.property_average_rating", { arguments: {} })
// @ts-expect-error extra key on a zero-argument function's args (EmptyObject)
void func("hotel.property_count", { arguments: { foo: 1 } })

// `set` is write-only, `get` read-only. `Parameters<write>` picks the last overload, the single-argument one.
const propertyAudit = relation("hotel.properties", {
  select: {
    id: ["col", "id"],
    audit_only: ["set", "chain_id"],
    display_only: ["get", "star_rating"],
  },
})

// [number] : one row of the root array.
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

// A relation or set-returning function root is an array.
export type _AssertPropertiesShapeIsArray = Expect<
  PropertiesShape extends readonly unknown[] ? true : false
>
export type _AssertRoomsAvailableShapeIsArray = Expect<
  RoomsAvailableShape extends readonly unknown[] ? true : false
>
// A scalar function root is its bare value, not an array.
const propertyCount = func("hotel.property_count", {})
export type PropertyCountShape = Awaited<ReturnType<typeof propertyCount.get>>
export type _AssertScalarFunctionRootIsNotArray = Expect<
  PropertyCountShape extends readonly unknown[] ? false : true
>
export type _AssertScalarFunctionRootIsNumber = Expect<
  PropertyCountShape extends Int8 ? true : false
>

// An unknown function (`never`) falls back to DefaultRow[], not `never`.
export type _AssertUnrecognizedFunctionFallsBackToDefaultRowArray = Expect<
  RootShapeFromFunctionMember<never, Record<string, never>> extends DefaultRow[] ? true : false
>

// An unknown name is rejected at the call site.
// @ts-expect-error unrecognized relation name
void relation("hotel.does_not_exist")
// @ts-expect-error unrecognized function name
void func("hotel.does_not_exist", {})
// @ts-expect-error unrecognized well-known query name
void wellknown("does_not_exist", {})

// A mixed overload set resolves each overload on its own : scalar to `returns`, set-returning to its row, never the
// raw `returns` array.
type PropertyAverageRatingResolved = ResolveModel<{ function: "hotel.property_average_rating" }>

// Distributive : only an array member gives `true`.
type ContainsArrayMember<T> = T extends readonly unknown[] ? true : never
// Distributive : only the set-returning variant's row matches `{ id: number }`.
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

// ["call", ...] types as the function's `returns`, by bare and by qualified name.
const propertyWithComputed = relation("hotel.properties", {
  select: {
    id: ["col", "id"],
    avg_bare: ["call", "property_average_rating", [".", "id"]],
    avg_qualified: ["call", { schema: "hotel", name: "property_average_rating" }, [".", "id"]],
  },
})

export type PropertyWithComputedShape = Awaited<ReturnType<typeof propertyWithComputed.get>>[number]
export type PropertyWithComputedWriteShape = Parameters<
  typeof propertyWithComputed.write
>[0][number]

// Only the scalar overload's member is checked ; the set-returning one stays an array here.
export type _AssertCallBareNameResolvesFunctionReturns = Expect<
  Extract<PropertyWithComputedShape["avg_bare"], PgNumeric> extends never ? false : true
>
export type _AssertCallQualifiedResolvesFunctionReturns = Expect<
  Extract<PropertyWithComputedShape["avg_qualified"], PgNumeric> extends never ? false : true
>
// A computed column is dropped from the write shape.
export type _AssertCallOmittedFromWriteShape = Expect<
  HasKey<PropertyWithComputedWriteShape, "avg_bare"> extends false ? true : false
>

// A zero-argument function's `args` rejects an object with keys.
export type _AssertZeroArgFunctionArgsRejectsArbitraryKeys = Expect<
  { foo: "bar" } extends Functions["hotel.property_count"]["args"] ? false : true
>

// RequiredColumns names "name"/"base_price" : those are required on write, "id" optional.
const roomTypeWrite = relation("hotel.room_types", {
  select: ["*~"],
})

export type RoomTypeWriteShape = Parameters<typeof roomTypeWrite.write>[0][number]

// No column type includes `undefined`, so only an optional key does.
type IsOptionalKey<T, K extends keyof T> = undefined extends T[K] ? true : false

export type _AssertRequiredColumnIsMandatory = Expect<
  IsOptionalKey<RoomTypeWriteShape, "name"> extends false ? true : false
>
export type _AssertNonRequiredColumnIsOptional = Expect<IsOptionalKey<RoomTypeWriteShape, "id">>

// A required column stays required under a renamed select key.
const roomTypeWriteAliased = relation("hotel.room_types", {
  select: { renamed_name: ["col", "name"], base_price: ["col", "base_price"] },
})

export type RoomTypeWriteAliasedShape = Parameters<typeof roomTypeWriteAliased.write>[0][number]

export type _AssertAliasedRequiredColumnStaysMandatory = Expect<
  IsOptionalKey<RoomTypeWriteAliasedShape, "renamed_name"> extends false ? true : false
>

// wellknown() takes its params upfront ; its shapes come from the embedded query literal.
const starRatedProperties = wellknown("properties_by_star_rating", { min_rating: 4 })

// A well-known query's root is an array too.
export type StarRatedPropertiesShape = Awaited<ReturnType<typeof starRatedProperties.get>>

export type _AssertWellknownShapeIsArray = Expect<
  StarRatedPropertiesShape extends readonly unknown[] ? true : false
>
export type _AssertWellknownShapeHasStarRating = Expect<
  HasKey<StarRatedPropertiesShape[number], "star_rating">
>

// call() fetches right away, so its checks sit in a function that is never run.
function _neverRun_callChecks() {
  // Both argument forms — positional tuple and named object — against the same set-returning function.
  void call("hotel.rooms_available", [1])
  void call("hotel.rooms_available", { property_id: 1 })

  // call() returns the matched overload's type, not a union of both.
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

  // A zero-argument function accepts `{}`.
  void call("hotel.property_count", {})

  // call() rejects arguments that don't match the declared ones.
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

// `this` inside `proto` sees the node's row ; the row type gets the `proto` members.
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

// Accessor helpers spread into `proto`.
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

// A `proto` member is never in the write shape, but draft() returns the row with them.
export type PropertyWithAccessorWriteShape = Parameters<
  typeof propertyWithAccessor.write
>[0][number]

export type _AssertWriteShapeHasNoAccessor = Expect<
  HasKey<PropertyWithAccessorWriteShape, "created_at_as_date"> extends false ? true : false
>
export type _AssertWriteShapeHasNoGetOnlyAccessor = Expect<
  HasKey<PropertyWithAccessorWriteShape, "location_as_point"> extends false ? true : false
>

function assertDraftRowAccessors() {
  const row = propertyWithAccessor.draft({ name: "x" })
  row.created_at_as_date = Temporal.Now.instant() // get+set : assignable
  // @ts-expect-error location_as_point is get-only, assigning it is a compile error on draft()'s row too
  row.location_as_point = { x: 0, y: 0 }
  // an draft() row is still accepted by write()
  void propertyWithAccessor.write([row])
}
void assertDraftRowAccessors

// moneyAccessor/bytesAccessor are get+set.
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

// A `proto` mixing a `get` and a method, each with an explicit return type, compiles with `this` typed.
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

// A join's `proto` merges into the join's row, and the parent's `this` sees it.
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

// A relation() Querier reused as a join's request keeps its `proto`.
const propertyWithReusedProto = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", roomWithProto),
  },
})

export type PropertyWithReusedProtoShape = Awaited<ReturnType<typeof propertyWithReusedProto.get>>

export type _AssertReusedProtoMemberIsVisible = Expect<
  HasKey<PropertyWithReusedProtoShape[number]["rooms"][number], "label">
>

// The same two cases through the scoped-join callback form.
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

// A get+set accessor is writable on the row.
function assertAccessorIsWritable(row: PropertyWithAccessorShape[number]) {
  row.created_at_as_date = Temporal.Now.instant()
}
void assertAccessorIsWritable

// draft() takes an outgoing (to-one) join as one nested row, not an array.
const createdRoom = rooms.draft({
  property_id: PROPERTY_1,
  room_type_id: ROOM_TYPE_1,
  room_number: "101",
  property: { name: "Marina Bay Grand Hotel" },
  room_type: { name: "Deluxe", base_price: asNumeric(129.99) },
})

export type _AssertCreateNestedToOneIsObject = Expect<
  typeof createdRoom.property extends readonly unknown[] ? false : true
>
export type _AssertCreateNestedToOneHasOwnColumn = Expect<HasKey<typeof createdRoom.property, "id">>

// draft() takes a to-many join as an array.
const createdProperty = propertyWithNestedProto.draft({
  name: "Marina Bay Grand Hotel",
  rooms: [{ property_id: PROPERTY_1, room_type_id: ROOM_TYPE_1, room_number: "101" }],
})

export type _AssertCreateNestedToManyIsArray = Expect<
  typeof createdProperty.rooms extends readonly unknown[] ? true : false
>

// draft()'s row carries the `proto` accessors, writable.
function assertCreatedRowAccessorIsWritable() {
  const createdPropertyWithAccessor = propertyWithAccessor.draft({
    name: "Marina Bay Grand Hotel",
  })
  createdPropertyWithAccessor.created_at_as_date = Temporal.Now.instant()
}
void assertCreatedRowAccessorIsWritable

// Pre-declared `as const` values are accepted.
const propertyInsertColumns = ["name", "star_rating"] as const
const propertyWhere = ["=", ["col", "star_rating"], 5] as const
const propertiesWithReusedConstants = relation("hotel.properties", {
  insert_columns: propertyInsertColumns,
  where: propertyWhere,
})
void propertiesWithReusedConstants

// "."/"dot" resolves a column, a join alias and a computed field, and a chain into a join's field.
const roomsWithDot = relation("hotel.rooms", (join) => ({
  select: {
    property_id: [".", "property_id"],
    room_type_name: [".", [".", "room_type"], "name"],
    // The flat chain resolves like the nested form above.
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

// A "." lookup of a to-many join is an array.
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

// On write, a single-hop "." to a column is writable like `col`.
export type RoomsWithDotWriteShape = Parameters<typeof roomsWithDot.write>[0][number]
export type _AssertDotColumnIsRequiredInWriteShape = Expect<
  HasKey<RoomsWithDotWriteShape, "property_id">
>
// A multi-hop chain is dropped from the write shape.
export type _AssertDotChainOmittedFromWriteShape = Expect<
  HasKey<RoomsWithDotWriteShape, "room_type_name"> extends false ? true : false
>

export type PropertyWithDotJoinWriteShape = Parameters<
  typeof propertyWithDotJoinAndComputed.write
>[0][number]
// A single-hop "." to a join is in the write shape.
export type _AssertDotJoinWritableInWriteShape = Expect<
  HasKey<PropertyWithDotJoinWriteShape, "rooms">
>
// A join is required in the write shape, also through a select map.
type IsRequiredKey<T, K extends keyof T> = Record<never, never> extends Pick<T, K> ? false : true
export type _AssertDotJoinIsRequiredInWriteShape = Expect<
  IsRequiredKey<PropertyWithDotJoinWriteShape, "rooms">
>
// A computed field through "." is dropped from the write shape.
export type _AssertDotComputedFieldOmittedFromWriteShape = Expect<
  HasKey<PropertyWithDotJoinWriteShape, "avg_rating"> extends false ? true : false
>

// Join nullability : "*" never null, "<" always nullable, ">" nullable iff a referencing column is.
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

// ">" over a NOT NULL column is not nullable.
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

// A nested select group holding a required column (`name`) is required on write.
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

// A nested group of only optional columns is optional.
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

// An unknown column, alias or computed field name is rejected in select/where.
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

// A computed field is referable through ".", but "*" doesn't include it.
const propertyWithValidComputedDot = relation("hotel.properties", {
  select: { avg: [".", "property_average_rating"] },
})
void propertyWithValidComputedDot
// @ts-expect-error typo of a real computed field name
void relation("hotel.properties", { select: { avg: [".", "property_average_ratingg"] } })

export type _AssertComputedFieldExcludedFromStarShape = Expect<
  HasKey<PropertiesBareShape[number], "property_average_rating"> extends false ? true : false
>

// A "." chain's hops past the leading name are not checked.
const roomsWithBogusHopButValidLeadingName = relation("hotel.rooms", (join) => ({
  select: { x: [".", "room_type", "this_hop_name_is_not_checked_against_anything"] },
  join: { room_type: join("hotel.room_types>id:room_type_id") },
}))
type RoomsWithBogusHopShape = Awaited<
  ReturnType<typeof roomsWithBogusHopButValidLeadingName.get>
>[number]
// An unknown hop types as `unknown`.
export type _AssertUncheckedHopFallsBackToUnknownAtShapeLevel = Expect<
  [RoomsWithBogusHopShape["x"]] extends [string] ? false : true
>

// A join written as an object literal with `shortcut` is checked like a join() call.
const propertyPlainJoin = relation("hotel.rooms", {
  join: {
    property: {
      shortcut: "hotel.properties>id:property_id",
      select: ["*~"],
    },
  },
})
export type PropertyPlainJoinShape = Awaited<ReturnType<typeof propertyPlainJoin.get>>
// Same row shape as the join()-built equivalent.
export type _AssertPlainJoinShapeHasTargetColumns = Expect<
  HasKey<PropertyPlainJoinShape[number]["property"], "name">
>

// The error lands on the object literal, not on the relation() call line.
void relation("hotel.rooms", {
  // @ts-expect-error "hotel.property_settings<property_id:id" isn't reachable from "hotel.rooms"
  join: { property: { shortcut: "hotel.property_settings<property_id:id" } },
})

// Each level's `shortcut` is checked against its own enclosing relation.
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

// The nested shortcut is valid from "hotel.properties" but not from rooms.
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

// A plain join entry's own join alias is referable from its select.
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

// "arr"/"lst" give a tuple ; "coalesce" the union of its arguments.
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

// A container entry is always optional on write, even over a required column.
export type RoomWithContainersWriteShape = Parameters<typeof roomWithContainers.write>[0][number]
export type _AssertContainerEntryStaysOptionalInWriteShape = Expect<
  IsRequiredKey<RoomWithContainersWriteShape, "coords"> extends false ? true : false
>
export type _AssertArrContainerWritableInWriteShape = Expect<
  HasKey<RoomWithContainersWriteShape, "coords">
>

// "*"/"*~" trailing arguments : an except-list drops columns, an and-map adds computed ones.
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

// On write, the and-map's "call" entry is dropped and the except-list still applies.
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

// ParsedShortcut (type level) and parseShortcut (runtime) agree on the shortcut `rooms`'s "property" join uses.
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
// Actually run, unlike the compile-only `assertXxx` helpers.
assertRuntimeParseMatchesTypeLevelParse()

// Through a "*" join, "room_type_id" stays required ; "property_id", filled by the join, is optional.
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

// draft() keeps what `data` carried : an optional `star_rating` passed in comes back required.
const draftBare = propertyWithNestedProto.draft({ name: "Marina Bay Grand Hotel", rooms: [] })
const draftWithRating = propertyWithNestedProto.draft({
  name: "Marina Bay Grand Hotel",
  star_rating: 5,
  rooms: [],
})

type OptionalKey<T, K extends keyof T> = Record<never, never> extends Pick<T, K> ? true : false

export type _AssertDraftOmittedStaysOptional = Expect<OptionalKey<typeof draftBare, "star_rating">>
export type _AssertDraftProvidedBecomesRequired = Expect<
  OptionalKey<typeof draftWithRating, "star_rating"> extends false ? true : false
>
// Declared column type, not the literal : the row stays assignable afterwards.
export type _AssertDraftProvidedKeepsColumnType = Expect<
  [typeof draftWithRating.star_rating] extends [number | null]
    ? [number | null] extends [typeof draftWithRating.star_rating]
      ? true
      : false
    : false
>
// An explicit `undefined` is not "provided".
const draftUndefined = propertyWithNestedProto.draft({
  name: "x",
  star_rating: undefined,
  rooms: [],
})
export type _AssertDraftUndefinedStaysOptional = Expect<
  OptionalKey<typeof draftUndefined, "star_rating">
>

// Still accepted by write(), the minimum viable shape.
void propertyWithNestedProto.write([draftWithRating])

// Joined rows get the same treatment, in a to-many array and in a to-one object.
const draftNestedMany = propertyWithNestedProto.draft({
  name: "x",
  rooms: [{ property_id: PROPERTY_1, room_type_id: ROOM_TYPE_1, room_number: "101", floor: 2 }],
})
export type _AssertDraftNestedManyProvidedBecomesRequired = Expect<
  OptionalKey<(typeof draftNestedMany.rooms)[number], "floor"> extends false ? true : false
>
// An empty array supplies no element, so the elements keep their write shape (optional columns stay optional) :
// a row built elsewhere can then be pushed into it.
const draftEmptyMany = propertyWithNestedProto.draft({ name: "x", rooms: [] })
export type _AssertDraftEmptyArrayKeepsElementOptional = Expect<
  OptionalKey<(typeof draftEmptyMany.rooms)[number], "floor">
>
const draftNestedOne = rooms.draft({
  property_id: PROPERTY_1,
  room_type_id: ROOM_TYPE_1,
  room_number: "101",
  property: { name: "x", star_rating: 3 },
  room_type: { name: "Deluxe", base_price: asNumeric(129.99) },
})
export type _AssertDraftNestedOneProvidedBecomesRequired = Expect<
  OptionalKey<typeof draftNestedOne.property, "star_rating"> extends false ? true : false
>

// An incoming join fills the joined row's referencing column itself, so a nested
// `rooms` row may omit `property_id` ; the same relation written on its own still requires it. Columns the join
// doesn't fill stay required.
const draftNestedOmitsJoinKey = propertyWithNestedProto.draft({
  name: "x",
  rooms: [{ room_type_id: ROOM_TYPE_1, room_number: "101" }],
})
void draftNestedOmitsJoinKey
function assertJoinKeyOnlyOptionalWhenJoinFillsIt() {
  propertyWithNestedProto.draft({
    name: "x",
    // @ts-expect-error room_type_id isn't filled by the join
    rooms: [{ room_number: "101" }],
  })
  // @ts-expect-error property_id is required when rooms is written without the outgoing join that fills it
  relation("hotel.rooms").draft({ room_type_id: ROOM_TYPE_1, room_number: "101" })
}
void assertJoinKeyOnlyOptionalWhenJoinFillsIt

// An outgoing join writes the referenced row first and threads its key into the referencing column, so a row
// that carries the join may omit that column : `rooms.property_id` (property) and `rooms.room_type_id` (room_type).
const draftOmitsOutgoingKeys = rooms.draft({
  room_number: "101",
  property: { name: "x" },
  room_type: { name: "d", base_price: asNumeric(1) },
})
void draftOmitsOutgoingKeys

// A proto accessor key is not accepted in `data` : it would shadow the accessor with a plain property.
function assertDraftRejectsAccessorKey() {
  propertyWithAccessor.draft({
    name: "x",
    // @ts-expect-error created_at_as_date is a proto member, not a column
    created_at_as_date: Temporal.Now.instant(),
  })
}
void assertDraftRejectsAccessorKey

// Bogus fields are rejected, at the root and inside joins. Each directive sits right above the offending key.
function assertDraftRejectsBogusFields() {
  propertyWithNestedProto.draft({
    name: "x",
    // @ts-expect-error `nmae` isn't a column
    nmae: "typo",
    rooms: [],
  })
  propertyWithNestedProto.draft({
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
  rooms.draft({
    property_id: PROPERTY_1,
    room_type_id: ROOM_TYPE_1,
    room_number: "1",
    // @ts-expect-error `bogus` isn't a column of the to-one joined row
    property: { name: "x", bogus: 1 },
    room_type: { name: "D", base_price: asNumeric(1) },
  })
}
void assertDraftRejectsBogusFields

// Table-local ids.
type PropertyId = Id["hotel.properties.id"]
type RoomTypeId = Id["hotel.room_types.id"]

// Assignable to the bare int type, never the other way round, never to another table's id.
export type _AssertIdWidensToBareNumber = Expect<PropertyId extends number ? true : false>
export type _AssertBareNumberIsNotAnId = Expect<number extends PropertyId ? false : true>
export type _AssertIdsOfDifferentTablesDiffer = Expect<PropertyId extends RoomTypeId ? false : true>

// Same for a bigserial : the id is an Int8 plus the table tag, spelled as generated (the fixture has no bigserial).
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
  const bigId = 5 as LedgerId
  const asInt8: Int8 = bigId
  void asInt8
}
void assertIdMixups

// A rejected value must not make draft() forget the other keys : only the offending key is reported, and the
// returned row still has `star_rating` required (it was supplied).
const draftWithBadValue = propertyWithNestedProto.draft({
  // @ts-expect-error name is a string
  name: 42,
  star_rating: 5,
  rooms: [],
})
export type _AssertDraftKeepsProvidedKeysDespiteBadValue = Expect<
  OptionalKey<typeof draftWithBadValue, "star_rating"> extends false ? true : false
>

///////////////////////////////////////////////////////////////////////
// draftNested()

const propertyTwoLevels = relation("hotel.properties", (join) => ({
  join: {
    rooms: join("hotel.rooms*id:property_id", (join) => ({
      proto: {
        get label() {
          return `${this.room_number} !`
        },
      },
      join: {
        room_type: join("hotel.room_types>id:room_type_id", { select: ["*~"] }),
      },
    })),
  },
}))

// To-many : one row, not an array ; the column the join fills is optional, the others stay required, and the
// join's own `proto` is attached.
const nestedRoom = propertyTwoLevels.draftNested(["rooms"], {
  room_type_id: ROOM_TYPE_1,
  room_number: "101",
  room_type: { name: "Deluxe", base_price: asNumeric(129.99) },
})
export type _AssertDraftNestedIsOneRow = Expect<
  typeof nestedRoom extends readonly unknown[] ? false : true
>
export type _AssertDraftNestedHasJoinProto = Expect<HasKey<typeof nestedRoom, "label">>
export type _AssertDraftNestedMatchesDraftType = Expect<
  [typeof nestedRoom] extends [(typeof propertyTwoLevels.$draft)["rooms"][number]] ? true : false
>

// A nested draft goes into its parent's array, and the parent is accepted by write().
function assertDraftNestedFitsParent() {
  const parent = propertyTwoLevels.draft({ name: "x", rooms: [] })
  parent.rooms.push(nestedRoom)
  void propertyTwoLevels.write([parent])
}
void assertDraftNestedFitsParent

// To-one, two levels deep : the row inside `rooms[].room_type`.
const nestedRoomType = propertyTwoLevels.draftNested(["rooms", "room_type"], {
  name: "Deluxe",
  base_price: asNumeric(129.99),
})
export type _AssertDraftNestedToOneIsObject = Expect<HasKey<typeof nestedRoomType, "name">>

function assertDraftNestedRejects() {
  // @ts-expect-error a column is not a join alias
  propertyTwoLevels.draftNested(["name"], {})
  // @ts-expect-error no such join
  propertyTwoLevels.draftNested(["nope"], {})
  // @ts-expect-error room_type isn't a join of the root
  propertyTwoLevels.draftNested(["room_type"], {})
  // @ts-expect-error room_type_id is not filled by the join
  propertyTwoLevels.draftNested(["rooms"], { room_number: "1" })
  propertyTwoLevels.draftNested(["rooms"], {
    room_type_id: ROOM_TYPE_1,
    room_number: "1",
    // @ts-expect-error `bogus` isn't a column of the nested row
    bogus: 1,
  })
}
void assertDraftNestedRejects

// `$row` / `$draft` are type-only handles ; the nested row type is indexed out of the root's.
export type _AssertRowHandleHasNestedRow = Expect<
  HasKey<(typeof propertyTwoLevels.$row)["rooms"][number], "room_number">
>
