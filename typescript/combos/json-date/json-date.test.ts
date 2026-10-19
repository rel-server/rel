// The `json,date` codec against a real server : the wire is JSON text, and the accessors turn it into a Date.
import { describe, expect, test } from "bun:test"
import { Querier, asTimestamptz, relation, timestampTzAccessor } from "./database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url

type Expect<T extends true> = T
type Equal<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false
const raw = (v: unknown): unknown => v

const samples = relation("hotel.codec_samples", {
  select: ["*~"],
  proto: { ...timestampTzAccessor("taken_tz") },
})
type Row = Awaited<ReturnType<typeof samples.get>>[number]
export type _Accessor = Expect<Equal<Row["taken_tz_as_date"], Date>>

const label = `json-date-${Math.random().toString(36).slice(2)}`

describe("the json,date codec", () => {
  test("the accessor reads text as a Date and writes a Date as text", async () => {
    const at = new Date("2026-10-18T14:30:00.123Z")
    const row = samples.draft({ label })
    row.taken_tz_as_date = at
    expect(typeof raw(row.taken_tz)).toBe("string")
    await samples.write([row])
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], label],
      proto: { ...timestampTzAccessor("taken_tz") },
    }).get()
    expect(typeof raw(read?.taken_tz)).toBe("string")
    expect(read?.taken_tz_as_date).toEqual(at)
  })

  test("a Date is accepted by the cast, a string needs an offset", () => {
    expect(raw(asTimestamptz(new Date("2026-10-18T14:30:00Z")))).toBe("2026-10-18T14:30:00.000Z")
    expect(() => asTimestamptz("2026-10-18T14:30:00")).toThrow()
  })
})
