// Compile-only fixture exercising relation()/join()/func() against the "hotel" example schema
// (schema.example.ts). specs/typescript.md ## Testing : until a proper runtime test harness exists, this file's
// only job is to fail `just check` if the generic machinery in querier.ts/shapes.ts stops accepting a
// well-formed query, or silently loosens some field's inferred type. Never calls .get()/.write() — those hit a
// real `fetch()` — so this file is safe to simply import, not just type-check.

import { func, join, relation } from "./querier"

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
