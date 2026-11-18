// What only the default `json,temporal` file does : Temporal accessors over the wire text, and a pluggable JSON
// implementation. Everything the codecs share is in ../trunk, run by every combo.
import { describe, expect, test } from "bun:test"
import {
  type Codec,
  Querier,
  type Int8,
  int8Accessor,
  relation,
  timestampTzAccessor,
} from "./generated/database"

// bun's expect() is strict about branded wire types (Int8, Timestamptz, ...) ; the brand is exactly what the
// runtime value does NOT carry, so comparisons go through `unknown`.
const raw = (v: unknown): unknown => v

// ---- proto accessors ----

const writableProperties = relation("hotel.properties", {
  proto: { ...timestampTzAccessor("created_at") },
})

const uniqueName = (prefix: string) =>
  `${prefix} ${Date.now()} ${Math.random().toString(36).slice(2)}`

describe("proto accessors", () => {
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
})

// ---- big numbers : the JSON implementation is replaceable ----

// int8 and numeric are bare numbers on the wire, with every digit Postgres has. The browser's JSON.parse rounds
// what a double cannot hold ; a JSON implementation that keeps it can be plugged in. This one is a minimal
// stand-in (it does not look inside strings) for lossless-json or json-bigint : integers of 16 digits or more
// become bigint, and a bigint is written back as a bare number.
const bigintJson: Codec = {
  decode: (text) =>
    JSON.parse((text as string).replace(/(?<![\w."])(-?\d{16,})(?![\w."])/g, '"$1n"'), (_k, v) =>
      typeof v === "string" && /^-?\d{16,}n$/.test(v) ? BigInt(v.slice(0, -1)) : v,
    ),
  encode: (value) =>
    JSON.stringify(value, (_k, v) => (typeof v === "bigint" ? `__bigint${v}__` : v)).replace(
      /"__bigint(-?\d+)__"/g,
      "$1",
    ),
}

// 2^53 + 1, the first integer a double cannot hold : written as a string literal and cast on the server
const beyondDouble = { big: ["::", "9007199254740993", "bigint"] } as const

describe("big numbers", () => {
  test("a plugged-in JSON implementation keeps every digit, for one query only", async () => {
    const exact = relation("hotel.properties", { select: beyondDouble, limit: 1 })
    exact.codec = bigintJson
    const [row] = await exact.get()
    expect(raw(row?.big)).toBe(9007199254740993n)

    expect(Object.hasOwn(relation("hotel.properties"), "codec")).toBe(false)
    expect(Querier.prototype.codec).not.toBe(bigintJson)
  })

  test("a bigint is sent as a bare number once the JSON implementation can write it", async () => {
    const byId = (id: unknown) =>
      relation("hotel.properties", {
        where: ["=", ["col", "id"], id as number],
        select: { name: ["col", "name"] },
      })
    const query = byId(1n)
    query.codec = bigintJson
    expect((await query.get()).map((r) => r.name)).toEqual(["Marina Bay Grand Hotel"])
  })

  test("int8Accessor reads a number as a bigint and stores a safe integer as a number", async () => {
    const accessors = int8Accessor("id")
    const holder = Object.defineProperties({ id: 7 as Int8 }, accessors) as {
      id: Int8
      id_as_bigint: bigint
    }
    expect(holder.id_as_bigint).toBe(7n)
    holder.id_as_bigint = 9n
    expect(raw(holder.id)).toBe(9)
    expect(typeof raw(holder.id)).toBe("number")
    holder.id_as_bigint = 9007199254740993n
    expect(raw(holder.id)).toBe(9007199254740993n)
  })
})
