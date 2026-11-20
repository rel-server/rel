// The trunk : what the client does, whatever the codec. test/tsclient copies it into each combos/<codec>/trunk and
// runs it there, typechecked against that codec's database.ts. A date and time, whose JS form depends on the codec,
// is compared through instantMs ; which form each codec gives is checked by the combo's own tests. The trunk runs
// once per codec against one database, so it asserts on the fixture rows (ids 1 to 3) and on rows it names uniquely.
import { describe, expect, test } from "bun:test"
import {
  type Int8,
  Querier,
  RelRequestError,
  asTimestamptz,
  call,
  func,
  join,
  relation,
  sequence,
  wellknown,
} from "../database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url

type Expect<T extends true> = T
type Equal<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false

// bun's expect() is strict about branded wire types (Int8, Timestamptz, ...) ; the brand is exactly what the
// runtime value does NOT carry, so comparisons go through `unknown`.
const raw = (v: unknown): unknown => v

// The milliseconds since the epoch of a date and time given as text, a Date or a Temporal.Instant.
const instantMs = (v: unknown): number =>
  typeof v === "string"
    ? Date.parse(v)
    : v instanceof Date
      ? v.getTime()
      : (v as { epochMilliseconds: number }).epochMilliseconds

const isFixture = (r: { id: unknown }) => Number(r.id) <= 3

let uniqueCounter = 0
const uniqueName = (prefix: string) =>
  `${prefix} ${Date.now()} ${Math.random().toString(36).slice(2)} ${uniqueCounter++}`

const samples = relation("hotel.codec_samples", { select: ["*~"] })

const propertiesBare = relation("hotel.properties")

describe("a bare relation", () => {
  test("returns the fixture rows, identity ids as numbers", async () => {
    const rows = (await propertiesBare.get()).filter(isFixture)
    expect(rows.map((r) => r.name)).toEqual([
      "Marina Bay Grand Hotel",
      "Harbour Lights Inn",
      "Lonely Cabin",
    ])
    expect(rows.map((r) => raw(r.id))).toEqual([1, 2, 3])
  })

  test("the wire type of every column matches its declared type", async () => {
    const [marina, , cabin] = (await propertiesBare.get()).filter(isFixture)
    if (!marina || !cabin) throw new Error("fixture rows missing")
    expect(typeof raw(marina.id)).toBe("number") // Int8 : a safe integer is a number in every codec
    expect(typeof marina.star_rating).toBe("number") // smallint
    expect(instantMs(marina.created_at)).toBe(Date.parse("2024-01-15T10:00:00Z")) // Timestamptz
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
  where: ["and", [">=", ["col", "star_rating"], 3], ["<=", ["col", "id"], 3]],
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

  test("a Date $param filters a timestamptz", async () => {
    // JSON sends it as ISO text, CBOR as tag 1
    const label = uniqueName("param")
    const at = "2026-10-19T08:00:00.250Z"
    await samples.write([samples.draft({ label, taken_tz: asTimestamptz(at) })])
    const since = relation("hotel.codec_samples", {
      select: { label: ["col", "label"] },
      where: [
        "and",
        ["=", ["col", "label"], label],
        [">=", ["col", "taken_tz"], ["$param", "from", "date"]],
      ],
    })
    expect(await since.get({ from: new Date(at) })).toEqual([{ label }])
    expect(await since.get({ from: new Date(Date.parse(at) + 1) })).toEqual([])
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
    const rows = (await propertiesWithRooms.get()).filter(isFixture)
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
    expect(raw(suite?.base_price)).toBe(349.5) // numeric : a number in every codec
  })

  test("a to-one join over a nullable foreign key is null when there is no row", async () => {
    const rows = (await propertiesWithChain.get()).filter(isFixture)
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

  test("array and enum columns keep their shape", async () => {
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

// ---- writes, draft() ----

const writableProperties = relation("hotel.properties")

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
    expect(typeof raw(written?.id)).toBe("number")
    expect(instantMs(written?.created_at)).toBeGreaterThan(Date.parse("2024-01-01T00:00:00Z"))

    const found = await relation("hotel.properties", {
      where: ["=", ["col", "name"], ["$param", "name", "string"]],
    }).get({ name })
    expect(found).toHaveLength(1)
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

// the body fetch is given, and the headers it is sent with
const sentBodies = async (run: () => Promise<unknown>) => {
  const sent: { encoding: string | null; size: number }[] = []
  const realFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const body = init?.body as string | Uint8Array
    sent.push({ encoding: new Headers(init?.headers).get("Content-Encoding"), size: body.length })
    return realFetch(input, init)
  }) as typeof fetch
  try {
    await run()
  } finally {
    globalThis.fetch = realFetch
  }
  return sent
}

describe("compressAbove", () => {
  test("a body over the threshold is sent gzipped and the server reads it", async () => {
    const name = uniqueName(`Gzipped ${"x".repeat(2000)}`)
    let written: unknown[] = []
    const sent = await sentBodies(async () => {
      written = await writableProperties.write([writableProperties.draft({ name, star_rating: 3 })])
    })
    expect(sent).toHaveLength(1)
    expect(sent[0]?.encoding).toBe("gzip")
    expect(sent[0]?.size).toBeLessThan(1024)
    expect((written[0] as { name: string }).name).toBe(name)
  })

  test("a body under the threshold, or on a query set to Infinity, goes as it is", async () => {
    const under = await sentBodies(() => relation("hotel.properties").get())
    const never = relation("hotel.properties")
    never.compressAbove = Number.POSITIVE_INFINITY
    const big = await sentBodies(() =>
      never.write([never.draft({ name: uniqueName(`Plain ${"x".repeat(2000)}`) })]),
    )
    expect(under[0]?.encoding).toBeNull()
    expect(big[0]?.encoding).toBeNull()
    expect(big[0]?.size).toBeGreaterThan(2000)
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

  test("a row leaving its join out leaves the join's rows alone, an empty array deletes them", async () => {
    const [roomType] = await relation("hotel.room_types").get()
    if (!roomType) throw new Error("fixture room type missing")
    const room = propertyWithRooms.draftNested(["rooms"], {
      room_type_id: roomType.id,
      room_number: "K-1",
    })
    const parent = propertyWithRooms.draft({ name: uniqueName("Keep rooms"), rooms: [] })
    parent.rooms.push(room)
    const [created] = await propertyWithRooms.write([parent])
    if (!created) throw new Error("nothing written")
    const again = relation("hotel.properties", {
      write_mode: "upsert",
      join: { rooms: join("hotel.properties", "hotel.rooms*id:property_id", { select: ["*~"] }) },
    })

    const [kept] = await again.write([{ id: created.id, name: created.name }])
    expect(kept?.rooms).toHaveLength(1)
    const [stillKept] = await again.write([
      { id: created.id, name: created.name, rooms: undefined },
    ])
    expect(stillKept?.rooms).toHaveLength(1)
    const [cleared] = await again.write([{ id: created.id, name: created.name, rooms: [] }])
    expect(cleared?.rooms).toHaveLength(0)
  })

  test("an outgoing join supplies the referencing column on write", async () => {
    const [roomType] = await relation("hotel.room_types").get()
    if (!roomType) throw new Error("fixture room type missing")
    const roomWithProperty = relation("hotel.rooms", {
      join: {
        property: join("hotel.rooms", "hotel.properties>id:property_id", { select: ["*~"] }),
      },
    })

    // no property_id : the nested property is written first and its key is used
    const name = uniqueName("Outgoing")
    const [written] = await roomWithProperty.write([
      roomWithProperty.draft({
        room_type_id: roomType.id,
        room_number: "O-1",
        property: { name },
      }),
    ])
    expect(written?.property.name).toBe(name)
    expect(raw(written?.property_id)).toBe(raw(written?.property.id))
  })

  test("an unknown join alias in the path throws", () => {
    expect(() => propertyWithRooms.draftNested(["nope"] as never, {} as never)).toThrow(
      /no join "nope"/,
    )
  })
})

// ---- where-driven writes : update and delete ----

describe("write_mode update and delete", () => {
  test("update applies one patch object to every row the where selects", async () => {
    const name = uniqueName("Patch me")
    await writableProperties.write([writableProperties.draft({ name, star_rating: 1 })])
    await writableProperties.write([writableProperties.draft({ name, star_rating: 1 })])

    const patchRating = relation("hotel.properties", {
      write_mode: "update",
      where: ["=", ["col", "name"], name],
      select: ["*~"],
    })
    // data is a patch : one object, any subset of the columns
    const patched = await patchRating.write({ star_rating: 5 })
    expect(patched.map((r) => r.star_rating)).toEqual([5, 5])
    // @ts-expect-error a patch is one object, not an array of rows
    void (() => patchRating.write([{ star_rating: 5 }]))
  })

  test("delete removes the rows the where selects and returns nothing", async () => {
    const name = uniqueName("Delete me")
    await writableProperties.write([writableProperties.draft({ name })])
    const byName = relation("hotel.properties", { where: ["=", ["col", "name"], name] })
    expect(await byName.get()).toHaveLength(1)

    const remove = relation("hotel.properties", {
      write_mode: "delete",
      where: ["=", ["col", "name"], name],
    })
    // no data to send : write() takes no argument
    expect(await remove.write()).toEqual([])
    expect(await byName.get()).toHaveLength(0)
  })

  test("a delete without a where is rejected", async () => {
    const remove = relation("hotel.properties", { write_mode: "delete" })
    const error = await remove.write().catch((e: unknown) => e)
    expect(error).toBeInstanceOf(RelRequestError)
    expect((error as RelRequestError).status).toBe(400)
    expect(await relation("hotel.properties").get()).not.toHaveLength(0)
  })
})

// ---- well-known queries ----

describe("well-known queries", () => {
  test("a param takes the value its type has in this codec : what a column of that type reads as", async () => {
    // the two latest of the rows codec_samples.sql seeds
    const latest = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], "bench"],
      order_by: [["desc", ["col", "id"]]],
      limit: 2,
    }).get()
    const from = latest[1]
    if (!from?.taken_tz || !from.content) throw new Error("bench rows missing")
    const rows = await wellknown("samples_since", {
      label: "bench",
      from: from.taken_tz,
      content: from.content,
    }).get()
    expect(rows.map((r) => Number(r.id))).toEqual(latest.map((r) => Number(r.id)).reverse())
  })
})

// ---- sequence() ----

describe("sequence()", () => {
  const labelled = relation("hotel.properties", {
    proto: {
      get label(): string {
        return `property ${this.name}`
      },
    },
  })

  // remembers a promise made inside a callback, to await once the sequence has settled
  const keep = (kept: Promise<unknown>[], promise: Promise<unknown>) => {
    kept.push(promise)
    return promise
  }

  test("a read, a write, function calls and a well-known query answer in one request, each with its own proto", async () => {
    const name = uniqueName("Sequence")
    const [sample] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], "bench"],
      order_by: [["desc", ["col", "id"]]],
      limit: 1,
    }).get()
    if (!sample?.taken_tz || !sample.content) throw new Error("bench rows missing")
    const since = { label: "bench", from: sample.taken_tz, content: sample.content }
    let result: unknown
    const sent = await sentBodies(async () => {
      const r = await sequence(() => [
        labelled.get(),
        labelled.write([labelled.draft({ name })]),
        roomsAvailable.get({ property_id: "1" }),
        call("hotel.rooms_available", { property_id: 1 as Int8 }),
        wellknown("samples_since", since).get(),
      ])
      type _ = Expect<
        Equal<
          typeof r,
          [
            Awaited<ReturnType<typeof labelled.get>>,
            Awaited<ReturnType<typeof labelled.get>>,
            Awaited<ReturnType<typeof roomsAvailable.get>>,
            (typeof r)[3],
            Awaited<ReturnType<ReturnType<typeof wellknown<"samples_since">>["get"]>>,
          ]
        >
      >
      result = r
    })
    expect(sent).toHaveLength(1)
    const [read, written, rooms, called, samples] = result as [
      { label: string }[],
      { name: string; label: string }[],
      { room_number: string }[],
      { room_number: string }[],
      { id: unknown }[],
    ]
    expect(read.find((r) => r.label === "property Marina Bay Grand Hotel")).toBeDefined()
    expect(written.map((r) => r.label)).toEqual([`property ${name}`])
    expect(rooms.map((r) => r.room_number).sort()).toEqual(["101", "102", "201"])
    expect(called.map((r) => r.room_number).sort()).toEqual(["101", "102", "201"])
    expect(samples.map((r) => Number(r.id))).toEqual([Number(sample.id)])
  })

  test("a failing item rolls back an earlier write and rejects every promise with its error", async () => {
    const name = uniqueName("Rolled back")
    const bad = { star_rating: 2 } as unknown as Parameters<
      typeof writableProperties.write
    >[0][number]
    const kept: Promise<unknown>[] = []
    const error = await sequence(() => [
      keep(kept, writableProperties.write([writableProperties.draft({ name })])),
      writableProperties.write([bad]),
    ]).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(RelRequestError)
    expect(kept).toHaveLength(1)
    expect(await kept[0]?.catch((e: unknown) => e)).toBe(error)
    const found = await relation("hotel.properties", { where: ["=", ["col", "name"], name] }).get()
    expect(found).toHaveLength(0)
  })

  test("a query the callback does not return is still sent, and its failure rejects the sequence", async () => {
    const name = uniqueName("Unreturned")
    const ok = await sequence(() => {
      writableProperties.write([writableProperties.draft({ name })])
      return [] as const
    })
    expect(ok).toEqual([])
    const found = await relation("hotel.properties", { where: ["=", ["col", "name"], name] }).get()
    expect(found).toHaveLength(1)

    const bad = { star_rating: 2 } as unknown as Parameters<
      typeof writableProperties.write
    >[0][number]
    const error = await sequence(() => {
      writableProperties.write([bad])
      return [] as const
    }).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(RelRequestError)
  })

  test("a different baseUrl or codec, an async callback and a nested sequence send nothing", async () => {
    const elsewhere = relation("hotel.properties")
    elsewhere.baseUrl = "http://127.0.0.1:1"
    const otherCodec = relation("hotel.properties")
    otherCodec.codec = { ...Querier.prototype.codec }
    const errors: unknown[] = []
    const sent = await sentBodies(async () => {
      // each callback records a first query, then fails
      const kept: Promise<unknown>[] = []
      const callbacks = [
        () => [keep(kept, propertiesBare.get()), elsewhere.get()],
        () => [keep(kept, propertiesBare.get()), otherCodec.get()],
        (async () => [keep(kept, propertiesBare.get())]) as never,
        () => [keep(kept, propertiesBare.get()), sequence(() => [propertiesBare.get()])],
      ] as (() => unknown[])[]
      for (const fn of callbacks) {
        const outcome = await sequence(fn).catch((e: unknown) => e)
        errors.push(outcome)
        expect(await kept.pop()?.catch((e: unknown) => e)).toBe(outcome)
      }
    })
    expect(sent).toHaveLength(0)
    expect(errors.map((e) => (e as Error).message)).toEqual([
      "sequence: every query must have the same baseUrl and codec as the first one",
      "sequence: every query must have the same baseUrl and codec as the first one",
      "sequence callback must be synchronous",
      "sequence() cannot be called inside another sequence's callback",
    ])
  })

  test("one item answers as a one-item tuple, and an empty sequence sends nothing", async () => {
    let one: unknown
    let none: unknown
    const sent = await sentBodies(async () => {
      one = await sequence(() => [labelled.get()])
      none = await sequence(() => [])
    })
    expect(sent).toHaveLength(1)
    const [rows] = one as [{ label: string }[]]
    expect(rows.find((r) => r.label === "property Marina Bay Grand Hotel")).toBeDefined()
    expect(none).toEqual([])
  })
})
