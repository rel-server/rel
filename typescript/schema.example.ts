// Draft/example of `specs/typescript.md ## File layout`'s section 2 — the database schema. A real deployment
// gets this section generated dynamically from introspection and inserted between querier.ts and query.ts/
// shapes.ts (never written to disk as its own file) ; this "hotel" fixture is kept in the repo so querier.ts/
// shapes.ts have something real to type-check and exercise against (see example.ts).

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

// For each function that is exported, we get an export
export interface Functions {
  "hotel.property_average_rating": {
    positional_args: [Table__Hotel__Properties]
    args: { property: Table__Hotel__Properties }
    returns: number
  }
  "hotel.rooms_available": {
    positional_args: [property_id: number, on_date?: Date]
    args: { property_id: number; on_date?: Date }
    relation: Table__Hotel__Properties // this function has an underlying
    returns: Table__Hotel__Properties[]
  }
}

// Well-known queries aren't introspectable yet — specs/migrations.md notes they aren't implemented server-side
// as of that document — so this stays empty until they are.
// biome-ignore lint/suspicious/noEmptyInterface: will be filled in once well-known queries are introspectable
export interface Wellknowns {}
