// The json-bigint preset against a real server : a bigint beyond 2^53 is read exactly and written as a bare number.
import "./json-bigint"
import { describe, expect, test } from "bun:test"
import { Querier, relation } from "./database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url

const raw = (v: unknown): unknown => v

describe("the json-bigint codec", () => {
  test("sends no content type, the server answers JSON", () => {
    expect(Querier.prototype.codec.contentType).toBeUndefined()
  })

  test("an int8 beyond 2^53 keeps every digit, a smaller one stays a number", async () => {
    // 2^53 + 1, the first integer a double cannot hold : written as a string literal and cast on the server
    const [row] = await relation("hotel.properties", {
      select: { big: ["::", "9007199254740993", "bigint"], id: ["col", "id"] },
      order_by: [["col", "id"]],
      limit: 1,
    }).get()
    expect(raw(row?.big)).toBe(9007199254740993n)
    expect(raw(row?.id)).toBe(1)
  })

  test("a bigint is sent as a bare number", async () => {
    const byId = relation("hotel.properties", {
      where: ["=", ["col", "id"], 1n as never],
      select: { name: ["col", "name"] },
    })
    expect((await byId.get()).map((r) => r.name)).toEqual(["Marina Bay Grand Hotel"])
  })

  test("a big int8 is written and read back", async () => {
    const samples = relation("hotel.codec_samples", { select: ["*~"] })
    const label = `bigint-${Math.random().toString(36).slice(2)}`
    await samples.write([samples.draft({ label, big: 9007199254740993n as never })])
    const [read] = await relation("hotel.codec_samples", {
      select: ["*~"],
      where: ["=", ["col", "label"], label],
    }).get()
    expect(raw(read?.big)).toBe(9007199254740993n)
  })
})
