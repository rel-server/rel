// The plain `json` codec against a real server : date and time types, bytea and an int8 are what JSON carries, as text
// and numbers, and a timestamp is written as text with an offset.
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
export type _TakenTz = Expect<Row["taken_tz"] extends string | null ? true : false>
export type _TakenOn = Expect<Row["taken_on"] extends string | null ? true : false>
export type _Content = Expect<Row["content"] extends string | null ? true : false>

const raw = (v: unknown): unknown => v
const label = `json-${Math.random().toString(36).slice(2)}`

describe("the json codec", () => {
  test("a row of every codec-dependent type is written as text and read back as text", async () => {
    await samples.write([
      samples.draft({
        label,
        taken_tz: asTimestamptz("2026-10-18T14:30:00.123+02:00"),
        taken_at: asTimestamp("2026-10-18T14:30:00"),
        taken_on: asPgDate("2026-10-18"),
      }),
    ])
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], label],
    }).get()
    expect(new Date(raw(read?.taken_tz) as string).toISOString()).toBe("2026-10-18T12:30:00.123Z")
    expect(raw(read?.taken_at)).toBe("2026-10-18T14:30:00")
    expect(raw(read?.taken_on)).toBe("2026-10-18")
  })

  test("a timestamptz string needs an offset", () => {
    expect(() => asTimestamptz("2026-10-18T14:30:00")).toThrow()
  })
})
