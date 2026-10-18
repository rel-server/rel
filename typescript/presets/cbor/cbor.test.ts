// The CBOR preset against a real server : what comes back is typed as PgWire says, and what is written round-trips.
import "./cbor"
import { describe, expect, spyOn, test } from "bun:test"
import {
  Querier,
  asPgDate,
  asTimestamp,
  asTimestamptz,
  dateAccessor,
  relation,
  timestampAccessor,
  timestampTzAccessor,
} from "./database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url

type Expect<T extends true> = T
type Equal<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false
const raw = (v: unknown): unknown => v

const samples = relation("hotel.codec_samples", { select: ["*~"] })
type Row = Awaited<ReturnType<typeof samples.get>>[number]

// the PgWire block of cbor.ts is what the column types say
export type _TakenTz = Expect<Row["taken_tz"] extends Temporal.Instant | null ? true : false>
export type _TakenAt = Expect<Row["taken_at"] extends Temporal.Instant | null ? true : false>
export type _TakenOn = Expect<Row["taken_on"] extends Temporal.PlainDate | null ? true : false>
export type _Content = Expect<Row["content"] extends Uint8Array | null ? true : false>
export type _Big = Expect<
  Equal<NonNullable<Row["big"]> extends number | bigint ? true : false, true>
>

const label = `cbor-${Math.random().toString(36).slice(2)}`

describe("the CBOR codec", () => {
  test("asks for CBOR and sends it", async () => {
    const spy = spyOn(globalThis, "fetch")
    try {
      await relation("hotel.properties", { select: ["*~"], limit: 1 }).get()
      const init = spy.mock.calls[0]?.[1] as RequestInit
      expect(init.headers).toEqual({
        "Content-Type": "application/cbor",
        Accept: "application/cbor",
      })
      expect(init.body).toBeInstanceOf(Uint8Array)
    } finally {
      spy.mockRestore()
    }
  })

  test("a timestamptz comes back as a Temporal.Instant, the accessor passing it through", async () => {
    const [marina] = await relation("hotel.properties", {
      select: ["*~"],
      order_by: [["col", "id"]],
    }).get()
    const created = raw(marina?.created_at)
    expect(created).toBeInstanceOf(Temporal.Instant)
    expect(
      (created as Temporal.Instant).equals(Temporal.Instant.from("2024-01-15T10:00:00Z")),
    ).toBe(true)
    const holder = Object.defineProperties(
      { created_at: marina?.created_at },
      timestampTzAccessor("created_at"),
    ) as unknown as {
      created_at_as_date: Temporal.Instant
    }
    expect(holder.created_at_as_date).toBe(created as Temporal.Instant)
  })

  test("a row of every codec-dependent type is written and read back", async () => {
    const instant = Temporal.Instant.from("2026-10-18T14:30:00.123456Z")
    const bytes = new Uint8Array([0, 1, 2, 254, 255])
    const row = samples.draft({
      label,
      taken_tz: asTimestamptz(instant),
      taken_at: asTimestamp(instant),
      taken_on: asPgDate(Temporal.PlainDate.from("2026-10-18")),
      content: bytes as never,
      big: 5_000_000_000 as never,
      price: 12.5 as never,
    })
    const [written] = await samples.write([row])
    expect(written).toBeDefined()
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], label],
    }).get()

    expect((raw(read?.taken_tz) as Temporal.Instant).equals(instant)).toBe(true)
    // a timestamp is read in the server's time zone, which the instant that was written is also relative to
    expect((raw(read?.taken_at) as Temporal.Instant).equals(instant)).toBe(true)
    expect(raw(read?.taken_on)).toBeInstanceOf(Temporal.PlainDate)
    expect((raw(read?.taken_on) as Temporal.PlainDate).toString()).toBe("2026-10-18")
    expect(raw(read?.content)).toBeInstanceOf(Uint8Array)
    expect(Array.from(raw(read?.content) as Uint8Array)).toEqual(Array.from(bytes))
    expect(BigInt(raw(read?.big) as number | bigint)).toBe(5_000_000_000n) // an 8-byte integer : a bigint
    expect(raw(read?.price)).toBe(12.5)
    expect(typeof raw(read?.token)).toBe("string")
    expect(raw(read?.token)).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    )
  })

  test("the date and timestamp accessors pass the Temporal values through", () => {
    const day = Temporal.PlainDate.from("2026-10-18")
    const date = Object.defineProperties(
      { taken_on: day },
      dateAccessor("taken_on"),
    ) as unknown as {
      taken_on_as_date: Temporal.PlainDate
    }
    expect(date.taken_on_as_date).toBe(day)
    const instant = Temporal.Instant.from("2026-10-18T14:30:00Z")
    const at = Object.defineProperties(
      { taken_at: instant },
      timestampAccessor("taken_at"),
    ) as unknown as {
      taken_at_as_date: Temporal.Instant
    }
    expect(at.taken_at_as_date).toBe(instant)
  })

  test("a member left undefined is dropped, as JSON.stringify would", async () => {
    const [written] = await samples.write([
      samples.draft({ label: `${label}-undef`, content: undefined as never }),
    ])
    expect(written).toBeDefined()
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], `${label}-undef`],
    }).get()
    expect(read?.content).toBeNull()
  })

  test("a failure is still a RelRequestError with the JSON body", async () => {
    const error = await relation("hotel.nope" as never)
      .get()
      .catch((e: unknown) => e)
    expect((error as { status: number }).status).toBe(400)
    expect(typeof (error as { body: string }).body).toBe("string")
  })
})
