// The example.ts queries (compile-only there, against the hand-written schema.example.ts), run for real against
// the hotel schema and test/hotel/fixture.sql : what comes back, and what its type says it is.
import { describe, expect, test } from "bun:test"
import {
  Querier,
  RelRequestError,
  func,
  join,
  relation,
  timestampTzAccessor,
} from "./generated/database"

type Expect<T extends true> = T
type Equal<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false

// bun's expect() is strict about branded wire types (Int8, Timestamptz, ...) ; the brand is exactly what the
// runtime value does NOT carry, so comparisons go through `unknown`.
const raw = (v: unknown): unknown => v

const propertiesBare = relation("hotel.properties")

describe("a bare relation", () => {
  test("returns every fixture row, identity ids as strings (bigint)", async () => {
    const rows = await propertiesBare.get()
    expect(rows.map((r) => r.name)).toEqual([
      "Marina Bay Grand Hotel",
      "Harbour Lights Inn",
      "Lonely Cabin",
    ])
    expect(rows.map((r) => raw(r.id))).toEqual(["1", "2", "3"])
  })

  test("the wire type of every column matches its declared type", async () => {
    const [marina, , cabin] = await propertiesBare.get()
    if (!marina || !cabin) throw new Error("fixture rows missing")
    expect(typeof raw(marina.id)).toBe("string") // Int8
    expect(typeof marina.star_rating).toBe("number") // smallint
    expect(raw(marina.created_at)).toBe("2024-01-15T10:00:00+00:00") // Timestamptz
    expect(raw(marina.location)).toBe("(103.86,1.28)") // PgPoint
    expect(cabin.star_rating).toBeNull()
    expect(cabin.location).toBeNull()
    expect(cabin.chain_id).toBeNull()
  })
})

// ---- select maps, params, where ----

const propertiesSelected = relation("hotel.properties", {
  select: {
    name: ["col", "name"],
    created: ["col", "created_at"],
    echoed: ["$param", "toto", "string"],
  },
  where: [">=", ["col", "star_rating"], 3],
  order_by: [["col", "name"]],
})

describe("unknown query keys", () => {
  test("a misspelled key is a type error, next to valid keys too", () => {
    void (() => {
      // @ts-expect-error `order` is not a query key (`order_by` is)
      relation("hotel.properties", { select: ["*"], order: [["col", "name"]] })
      // @ts-expect-error same, in the callback form
      relation("hotel.properties", () => ({ select: ["*"], limitt: 1 }))
      relation("hotel.properties", {
        join: {
          rooms: join("hotel.properties", "hotel.rooms*id:property_id", {
            // @ts-expect-error a join's own query is checked too (reported on the literal's first key)
            select: ["*"],
            bogus: 1,
          }),
        },
      })
      // @ts-expect-error and so is func()
      func("hotel.search_properties", { arguments: { query: "x" }, select: ["*"], bogus: 1 })
    })
  })
})

describe("joins that exist but aren't allowed", () => {
  // hotel.rooms.room_type_id has no index, so rooms and room_types can't be joined, in either direction. The
  // compiler's message carries the reason : `{ error: "hotel.rooms (room_type_id) has no index, ..." }`.
  test("an unindexed foreign key is a type error, from either side and in every join form", () => {
    void (() => {
      // @ts-expect-error hotel.rooms (room_type_id) has no index
      join("hotel.rooms", "hotel.room_types>id:room_type_id")
      // @ts-expect-error same, from the other side
      join("hotel.room_types", "hotel.rooms*id:room_type_id")
      relation("hotel.rooms", (join) => ({
        // @ts-expect-error same, through the scoped join
        join: { rt: join("hotel.room_types>id:room_type_id") },
      }))
      // an indexed one next to it is fine
      join("hotel.rooms", "hotel.properties>id:property_id")
    })
  })
})

describe("select map, $param, where", () => {
  test("only the selected columns come back, the param is substituted, where filters", async () => {
    const rows = await propertiesSelected.get({ toto: "hello" })
    expect(rows.map((r) => r.name)).toEqual(["Harbour Lights Inn", "Marina Bay Grand Hotel"])
    expect(rows.every((r) => raw(r.echoed) === "hello")).toBe(true)
    expect(Object.keys(rows[0] ?? {}).sort()).toEqual(["created", "echoed", "name"])
  })

  test("the same query reused with another param", async () => {
    const rows = await propertiesSelected.get({ toto: "again" })
    expect(raw(rows[0]?.echoed)).toBe("again")
  })

  test("params are required when the query has $param, optional when it has none", () => {
    // @ts-expect-error `toto` is a required param of this query
    void (() => propertiesSelected.get())
    void (() => propertiesBare.get()) // none : may be called bare
  })

  test("the row type holds exactly the selected keys", () => {
    type Row = Awaited<ReturnType<typeof propertiesSelected.get>>[number]
    type _ = Expect<Equal<keyof Row, "name" | "created" | "echoed">>
  })
})

// ---- joins ----

const propertiesWithRooms = relation("hotel.properties", {
  join: {
    rooms: join("hotel.properties", "hotel.rooms*id:property_id", { select: ["*"] }),
  },
})

const roomTypesWithProperty = relation("hotel.room_types", {
  join: {
    property: join("hotel.room_types", "hotel.properties>id:property_id", { select: ["*~"] }),
  },
})

const propertiesWithChain = relation("hotel.properties", {
  join: {
    chain: join("hotel.properties", "hotel.chains>id:chain_id", { select: ["*~"] }),
  },
})

// chains -> properties -> rooms : a scoped join, two levels deep
const chainsNested = relation("hotel.chains", (join) => ({
  join: {
    properties: join("hotel.properties*id:chain_id", (join) => ({
      select: ["*"], // "*~" would leave the nested join out of the result
      join: { rooms: join("hotel.rooms*id:property_id", { select: ["*~"] }) },
    })),
  },
}))

describe("joins", () => {
  test("a to-many join is an array, empty when there is nothing to join", async () => {
    const rows = await propertiesWithRooms.get()
    const byName = Object.fromEntries(rows.map((r) => [r.name, r.rooms.length]))
    expect(byName).toEqual({
      "Marina Bay Grand Hotel": 3,
      "Harbour Lights Inn": 1,
      "Lonely Cabin": 0,
    })
    type Rooms = Awaited<ReturnType<typeof propertiesWithRooms.get>>[number]["rooms"]
    type _ = Expect<Equal<Rooms extends readonly unknown[] ? true : false, true>>
  })

  test("a to-one join is a single nested row", async () => {
    const rows = await roomTypesWithProperty.get()
    const suite = rows.find((r) => r.name === "Suite")
    expect(suite?.property.name).toBe("Marina Bay Grand Hotel")
    expect(Array.isArray(suite?.property)).toBe(false)
    expect(raw(suite?.base_price)).toBe("349.50") // numeric : a string on the wire, never a float
  })

  test("a to-one join over a nullable foreign key is null when there is no row", async () => {
    const rows = await propertiesWithChain.get()
    const byName = Object.fromEntries(rows.map((r) => [r.name, r.chain?.name ?? null]))
    expect(byName).toEqual({
      "Marina Bay Grand Hotel": "Grand Chain",
      "Harbour Lights Inn": "Grand Chain",
      "Lonely Cabin": null,
    })
  })

  test("a scoped join, two levels deep", async () => {
    const [chain] = await chainsNested.get()
    const marina = chain?.properties.find((p) => p.name === "Marina Bay Grand Hotel")
    expect(marina?.rooms.map((r) => r.room_number).sort()).toEqual(["101", "102", "201"])
  })

  test("array and enum columns keep their JSON shape", async () => {
    const rooms = await relation("hotel.rooms").get()
    const room = rooms.find((r) => r.room_number === "201")
    expect(room?.features).toEqual(["wifi", "balcony", "jacuzzi"])
    expect(room?.status).toBe("clean")
    expect(raw(room?.floor)).toBe(2)
  })
})

// ---- functions ----

const searchProperties = func("hotel.search_properties", {
  arguments: { query: ["$param", "q", "string"] },
  select: ["*"],
})

const roomsAvailable = func("hotel.rooms_available", {
  arguments: { property_id: ["$param", "property_id", "string"] },
  select: ["*"],
})

describe("functions", () => {
  test("a set-returning function with a $param argument", async () => {
    const hits = await searchProperties.get({ q: "pool" })
    expect(hits.map((r) => r.name)).toEqual(["Marina Bay Grand Hotel"])
    expect(await searchProperties.get({ q: "harbour" })).toHaveLength(1)
    expect(await searchProperties.get({ q: "nonexistentword" })).toHaveLength(0)
  })

  test("a function returning rows of a relation", async () => {
    const rows = await roomsAvailable.get({ property_id: "1" })
    expect(rows.map((r) => r.room_number).sort()).toEqual(["101", "102", "201"])
  })
})

// ---- writes, draft(), proto accessors ----

const writableProperties = relation("hotel.properties", {
  proto: { ...timestampTzAccessor("created_at") },
})

let uniqueCounter = 0
const uniqueName = (prefix: string) => `${prefix} ${Date.now()} ${uniqueCounter++}`

describe("draft() and write()", () => {
  test("a row built with draft() round-trips through write()", async () => {
    const name = uniqueName("Round trip")
    const row = writableProperties.draft({ name, star_rating: 4 })
    // draft() kept what was passed : star_rating is required on the returned row
    type _ = Expect<Equal<undefined extends (typeof row)["star_rating"] ? true : false, false>>
    expect(row.star_rating).toBe(4)

    const [written] = await writableProperties.write([row])
    expect(written?.name).toBe(name)
    expect(written?.star_rating).toBe(4)
    // the server filled in what we left out
    expect(typeof raw(written?.id)).toBe("string")
    expect(typeof raw(written?.created_at)).toBe("string")

    const found = await relation("hotel.properties", {
      where: ["=", ["col", "name"], ["$param", "name", "string"]],
    }).get({ name })
    expect(found).toHaveLength(1)
  })

  test("a writable accessor assigned after draft() reaches the server through its setter", async () => {
    const row = writableProperties.draft({ name: uniqueName("Accessor") })
    // not an own property : the prototype's setter runs, and writes the underlying column
    expect(Object.keys(row)).toEqual(["name"])
    row.created_at_as_date = Temporal.Instant.from("2020-01-01T00:00:00Z")
    expect(Object.keys(row).sort()).toEqual(["created_at", "name"])
    expect(JSON.stringify(row)).not.toContain("created_at_as_date")

    const [written] = await writableProperties.write([row])
    expect(raw(written?.created_at)).toBe("2020-01-01T00:00:00+00:00")
    expect(written?.created_at_as_date?.toString()).toBe("2020-01-01T00:00:00Z")
  })

  test("the proto accessor derives a Temporal.Instant from the wire string, on a fetched row too", async () => {
    const [marina] = await writableProperties.get()
    const instant = marina?.created_at_as_date
    expect(instant).toBeInstanceOf(Temporal.Instant)
    expect(instant?.toString()).toBe("2024-01-15T10:00:00Z")
  })

  test("a rejected write throws RelRequestError carrying the status", async () => {
    // the type system stops this (name is required), so go around it
    const row = { star_rating: 2 } as unknown as Parameters<
      typeof writableProperties.write
    >[0][number]
    const error = await writableProperties.write([row]).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(RelRequestError)
    expect((error as RelRequestError).status).toBeGreaterThanOrEqual(400)
  })
})

describe("baseUrl", () => {
  test("a query can point at another server without touching the others", async () => {
    const elsewhere = relation("hotel.properties")
    elsewhere.baseUrl = "http://127.0.0.1:1" // nothing listens there
    await expect(elsewhere.get()).rejects.toThrow()
    // the default is untouched
    expect(await relation("hotel.properties").get()).not.toHaveLength(0)
    expect(Object.hasOwn(relation("hotel.properties"), "baseUrl")).toBe(false)
  })

  test("a trailing slash is ignored", async () => {
    const q = relation("hotel.properties")
    q.baseUrl = `${Querier.prototype.baseUrl}/`
    expect(await q.get()).not.toHaveLength(0)
  })
})

describe("draftNested()", () => {
  const propertyWithRooms = relation("hotel.properties", {
    join: {
      rooms: join("hotel.properties", "hotel.rooms*id:property_id", {
        select: ["*~"],
        proto: {
          get label(): string {
            return `room ${this.room_number}`
          },
        },
      }),
    },
  })

  test("builds a nested row with the join's proto, written through the parent", async () => {
    const [roomType] = await relation("hotel.room_types").get()
    if (!roomType) throw new Error("fixture room type missing")

    // no property_id : the parent supplies it on write
    const room = propertyWithRooms.draftNested(["rooms"], {
      room_type_id: roomType.id,
      room_number: "N-1",
    })
    expect(room.label).toBe("room N-1")

    // a nested draft carries proto members, which draft()'s own data may not : it goes in through the parent's array
    const parent = propertyWithRooms.draft({ name: uniqueName("Nested"), rooms: [] })
    parent.rooms.push(room)
    const [written] = await propertyWithRooms.write([parent])
    expect(written?.rooms).toHaveLength(1)
    expect(written?.rooms[0]?.room_number).toBe("N-1")
    expect(written?.rooms[0]?.label).toBe("room N-1")
  })

  test("an unknown join alias in the path throws", () => {
    expect(() => propertyWithRooms.draftNested(["nope"] as never, {} as never)).toThrow(
      /no join "nope"/,
    )
  })
})
