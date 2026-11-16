// Example of the generated schema section, which is generated from introspection and placed between querier.ts and
// query.ts/shapes.ts. This "hotel" fixture gives querier.ts/shapes.ts something to type-check against (example.ts).
import type { ResolveModel, RootShapeFromLiteralQuery, WriteShapeFromRelationQuery } from "./shapes"
import type { Id, Int8, Money, PgDate, PgNumeric, PgPoint, Timestamptz } from "./pg_values"

// tsgen generates this, not `{}`, for an empty object shape (zero-argument `args`, zero-column relation). Not for the
// top-level interfaces (Relations, Functions, ...) : their keyof must stay `never` when empty.
type EmptyObject = Record<string, never>

// One entry per serial/identity column. Module augmentation only because this fixture is a separate module.
declare module "./pg_values" {
  interface Id {
    "hotel.properties.id": number & { readonly __id: "hotel.properties.id" }
    "hotel.room_types.id": number & { readonly __id: "hotel.room_types.id" }
    "hotel.property_settings.id": number & { readonly __id: "hotel.property_settings.id" }
  }
}

// A pg enum : Type__<Schema>__<Name>, same as Table__/View__.
type Type__Hotel__RoomStatus = "clean" | "dirty"

interface Table__Hotel__Rooms {
  /* same goes for columns */
  property_id: Id["hotel.properties.id"] // not null references hotel.properties (id),
  room_type_id: Id["hotel.room_types.id"] // not null references hotel.room_types (id),
  room_number: string
  floor: number | null
  status: Type__Hotel__RoomStatus // default 'clean', //: should we handle default creation client-side ?
  features: string[] // default '{}'
}

interface Table__Hotel__Properties {
  /** And comments for each column are added */
  id: Id["hotel.properties.id"]
  chain_id: number | null
  name: string
  location: PgPoint
  star_rating: number | null
  description: string | null
  created_at: Timestamptz | null
}

// Gives `Relationships["hotel.rooms"]` a second variant to disambiguate.
interface Table__Hotel__RoomTypes {
  id: Id["hotel.room_types.id"]
  name: string
  base_price: PgNumeric
  deposit: Money | null
  photo: string | null // bytea, hex form, unbranded
}

// Tests an incoming-and-unique ("<") join : `property_id` is unique, so from `hotel.properties` the join is always
// nullable.
interface Table__Hotel__PropertySettings {
  id: Id["hotel.property_settings.id"]
  property_id: Id["hotel.properties.id"] // not null, unique references hotel.properties (id)
  currency: string
}

export interface Relations {
  "hotel.rooms": Table__Hotel__Rooms
  "hotel.properties": Table__Hotel__Properties
  "hotel.room_types": Table__Hotel__RoomTypes
  "hotel.property_settings": Table__Hotel__PropertySettings
}

// The columns a write must supply : not nullable, no default, not identity/generated.
export interface RequiredColumns {
  "hotel.properties": "name"
  "hotel.rooms": "property_id" | "room_type_id" | "room_number"
  "hotel.room_types": "name" | "base_price"
  "hotel.property_settings": "property_id" | "currency"
}

// One variant per foreign key reachable from the relation, picked by `shortcut`. The shortcut is client-side only
// and fills in `on`/`relation`/`schema`. Its marker gives the cardinality : `>` outgoing (to-one), `<` incoming and
// unique (to-one), `*` incoming (to-many). Pairs read `<joined relation's column>:<enclosing relation's column>`,
// like `on`, whichever side owns the foreign key.
export interface Relationships {
  "hotel.rooms":
    | {
        shortcut: "hotel.properties>id:property_id" // rooms.property_id -> properties.id
        relation: Table__Hotel__Properties // the join's row type
      }
    | {
        shortcut: "hotel.room_types>id:room_type_id" // rooms.room_type_id -> room_types.id
        relation: Table__Hotel__RoomTypes
      }
  "hotel.properties":
    | {
        shortcut: "hotel.rooms*id:property_id" // rooms.property_id -> properties.id, viewed from properties ; to-many
        relation: Table__Hotel__Rooms
      }
    | {
        // Contrived, to test an outgoing join over a nullable column (chain_id), which must type as nullable.
        shortcut: "hotel.properties>id:chain_id" // properties.chain_id -> properties.id
        relation: Table__Hotel__Properties
      }
    | {
        shortcut: "hotel.property_settings<property_id:id" // property_settings.property_id -> properties.id ; 1:1
        relation: Table__Hotel__PropertySettings
      }
  "hotel.room_types": {
    shortcut: "hotel.rooms*id:room_type_id" // rooms.room_type_id -> room_types.id, viewed from room_types ; to-many
    relation: Table__Hotel__Rooms
  }
  "hotel.property_settings": {
    // NOT NULL, so this outgoing join is non-nullable.
    shortcut: "hotel.properties>id:property_id" // property_settings.property_id -> properties.id
    relation: Table__Hotel__Properties
  }
}

// The foreign keys that can't be joined because the referencing columns aren't indexed : same spelling as
// Relationships, plus the `reason` join() reports. Empty in this fixture.
// biome-ignore lint/suspicious/noEmptyInterface: always emitted, see renderRelationships's doc comment
export interface IneligibleRelationships {}

// Computed fields : per relation, the bare function names callable on its row with no other argument, and what each
// returns. property_average_rating's second overload takes a `number`, so it is not listed.
interface Computed__Hotel__Properties {
  property_average_rating: PgNumeric
}

export interface ComputedProperties {
  "hotel.properties": Computed__Hotel__Properties
}

// For each function that is exported, we get an export
export interface Functions {
  // Overloaded, unioned. The second variant is contrived, to test a mixed scalar/set-returning overload set.
  "hotel.property_average_rating":
    | {
        positional_args: [Table__Hotel__Properties]
        args: { property: Table__Hotel__Properties }
        returns: PgNumeric
      }
    | {
        positional_args: [property_id: number]
        args: { property_id: number }
        relation: Table__Hotel__Properties
        returns: Table__Hotel__Properties[]
      }
  "hotel.rooms_available": {
    positional_args: [property_id: number, on_date?: PgDate]
    args: { property_id: number; on_date?: PgDate }
    relation: Table__Hotel__Properties // this function has an underlying
    returns: Table__Hotel__Properties[]
  }
  // Zero-argument function : `args` is EmptyObject.
  "hotel.property_count": {
    positional_args: []
    args: EmptyObject
    returns: Int8
  }
}

// Unqualified name (resolved through search_path) -> Functions entry, to type `["call", "name", ...]`.
export interface FunctionsByName {
  property_average_rating: Functions["hotel.property_average_rating"]
  rooms_available: Functions["hotel.rooms_available"]
}

// Well-known queries : each query's JSON is embedded `as const`, and its shapes are inferred from that literal.
const __wellknown_properties_by_star_rating_query = {
  schema: "hotel",
  relation: "properties",
  where: ["=", "star_rating", ["$param", "min_rating", "int"]],
} as const

export interface Wellknowns {
  properties_by_star_rating: {
    params: { min_rating: number }
    shape: RootShapeFromLiteralQuery<typeof __wellknown_properties_by_star_rating_query>
    // A plain `[]` wrap : a function-rooted query is never writable.
    write_shape: WriteShapeFromRelationQuery<
      typeof __wellknown_properties_by_star_rating_query,
      ResolveModel<typeof __wellknown_properties_by_star_rating_query>
    >[]
  }
}
