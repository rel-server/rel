// The cbor-date preset against a real server : instants are Dates, dates and uuids stay text, bytea is bytes.
import "./cbor-date"
import { describe, expect, test } from "bun:test"
import { Querier, asPgDate, asTimestamp, asTimestamptz, relation } from "./database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url

type Expect<T extends true> = T
const samples = relation("hotel.codec_samples", { select: ["*~"] })
type Row = Awaited<ReturnType<typeof samples.get>>[number]
export type _TakenTz = Expect<Row["taken_tz"] extends Date | null ? true : false>
export type _TakenAt = Expect<Row["taken_at"] extends Date | null ? true : false>
export type _TakenOn = Expect<Row["taken_on"] extends string | null ? true : false>
export type _Content = Expect<Row["content"] extends Uint8Array | null ? true : false>

const raw = (v: unknown): unknown => v
const label = `cbor-date-${Math.random().toString(36).slice(2)}`

describe("the cbor-date codec", () => {
  test("a row of every codec-dependent type is written and read back", async () => {
    const at = new Date("2026-10-18T14:30:00.123Z")
    const bytes = new Uint8Array([0, 1, 2, 254, 255])
    await samples.write([
      samples.draft({
        label,
        taken_tz: asTimestamptz(at),
        taken_at: asTimestamp(at as never),
        taken_on: asPgDate("2026-10-18"),
        content: bytes as never,
        big: 5_000_000_000 as never,
      }),
    ])
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], label],
    }).get()
    expect(raw(read?.taken_tz)).toEqual(at)
    expect(raw(read?.taken_at)).toEqual(at) // the session zone is UTC here
    expect(raw(read?.taken_on)).toBe("2026-10-18")
    expect(Array.from(raw(read?.content) as Uint8Array)).toEqual(Array.from(bytes))
    expect(BigInt(raw(read?.big) as number | bigint)).toBe(5_000_000_000n)
    expect(raw(read?.token)).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    )
  })

  test("a date reads as Postgres spells it, far from Date's own range too", async () => {
    const [row] = await relation("hotel.properties", {
      select: {
        far: ["::", "5874897-12-31", "date"],
        bc: ["::", "0044-03-15 BC", "date"],
        epoch: ["::", "1970-01-01", "date"],
      },
      limit: 1,
    }).get()
    expect(raw(row?.far)).toBe("5874897-12-31")
    expect(raw(row?.bc)).toBe("0044-03-15 BC")
    expect(raw(row?.epoch)).toBe("1970-01-01")
  })

  test("an asTimestamptz string needs an offset", () => {
    expect(() => asTimestamptz("2026-10-18T14:30:00")).toThrow()
    expect(raw(asTimestamptz("2026-10-18T14:30:00Z"))).toEqual(new Date("2026-10-18T14:30:00Z"))
  })
})
