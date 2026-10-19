// The `cbor,date` codec against a real server : instants are Dates, dates and uuids stay text, bytea is bytes.
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

  describe("structures", () => {
    // what one request puts on the wire : its Accept header, its body, and the Content-Type of the answer
    async function wire<T>(fn: () => Promise<T>) {
      const seen: { accept?: string; body?: Uint8Array; contentType?: string | null; answer?: Uint8Array } = {}
      const real = globalThis.fetch
      globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
        seen.accept = (init?.headers as Record<string, string> | undefined)?.Accept
        seen.body = init?.body as Uint8Array
        const res = await real(input, init)
        seen.contentType = res.headers.get("Content-Type")
        seen.answer = new Uint8Array(await res.clone().arrayBuffer())
        return res
      }) as typeof fetch
      try {
        return { value: await fn(), ...seen }
      } finally {
        globalThis.fetch = real
      }
    }
    // tag 0xdfff, which opens a structure's definition
    const hasDefinition = (b: Uint8Array | undefined) => {
      for (let i = 0; b && i + 2 < b.length; i++) if (b[i] === 0xd9 && b[i + 1] === 0xdf && b[i + 2] === 0xff) return true
      return false
    }

    const rows = () =>
      relation("hotel.codec_samples", {
        select: ["*~"],
        where: ["like", ["col", "label"], "structures-%"],
        order_by: [["col", "id"]],
      })
    const rowsWritten = 30

    test("responses are structures by default, and a row reads the same as with maps", async () => {
      const q = samples
      await q.write(
        Array.from({ length: rowsWritten }, (_, i) =>
          q.draft({ label: `structures-${i}`, big: i as never } as never),
        ) as never[],
      )
      const on = await wire(() => rows().get())
      expect(on.accept).toBe("application/cbor; structures=1")
      expect(on.contentType).toBe("application/cbor; structures=1")
      expect(hasDefinition(on.answer)).toBe(true)

      const codec = Querier.prototype.codec
      codec.accept = "application/cbor"
      try {
        const off = await wire(() => rows().get())
        expect(off.accept).toBe("application/cbor")
        expect(off.contentType).toBe("application/cbor")
        expect(hasDefinition(off.answer)).toBe(false)
        expect(off.answer!.length).toBeGreaterThan(on.answer!.length)
        expect(off.value).toEqual(on.value)
      } finally {
        codec.accept = "application/cbor; structures=1"
      }
      expect(on.value.length).toBe(rowsWritten)
    })

    test("requests are structures by default, and sendStructures turns that off for one query", async () => {
      const q = samples
      const draft = (i: number) => q.draft({ label: `structures-w-${i}`, big: i as never } as never)
      const on = await wire(() => q.write(Array.from({ length: 10 }, (_, i) => draft(i)) as never[]))
      expect(hasDefinition(on.body)).toBe(true)

      const off = await wire(async () => {
        const one = relation("hotel.codec_samples", { select: ["*~"] })
        one.sendStructures = false
        return one.write(Array.from({ length: 10 }, (_, i) => draft(10 + i)) as never[])
      })
      expect(hasDefinition(off.body)).toBe(false)
      expect(off.body!.length).toBeGreaterThan(on.body!.length)

      // another query is not affected
      const again = await wire(() => q.write([draft(20)] as never[]))
      expect(hasDefinition(again.body)).toBe(true)

      const [got] = await relation("hotel.codec_samples", {
        select: ["*~"],
        where: ["=", ["col", "label"], "structures-w-12"],
      }).get()
      expect(got?.label).toBe("structures-w-12")
    })

    test("a jsonb value with keys of its own is written and read back", async () => {
      const docs = Array.from({ length: 20 }, (_, i) => ({ ["k" + i]: i, same: "x", nested: { a: i } }))
      const written = relation("hotel.codec_samples", { select: ["*~"] })
      for (const sendStructures of [true, false]) {
        written.sendStructures = sendStructures
        const label = `structures-jsonb-${sendStructures}`
        await written.write(
          docs.map((meta, i) => written.draft({ label: `${label}-${i}`, meta } as never)) as never[],
        )
        const read = await relation("hotel.codec_samples", {
          select: ["*~"],
          where: ["like", ["col", "label"], `${label}-%`],
        }).get()
        expect(read.length).toBe(docs.length)
        const metas = read.map((r) => (r as { meta: unknown }).meta)
        for (const doc of docs) expect(metas).toContainEqual(doc)
      }
    })
  })
})
