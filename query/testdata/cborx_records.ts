// Prints one line per document : the hex cbor-x writes with the record extension on, and the JSON of what cbor-x
// itself reads back. query/node_cbor_cborx_test.go checks that rel's reader gives the same value.
import { Decoder, Encoder } from "../../typescript/node_modules/cbor-x/index.js"

const docs: Record<string, unknown> = {}
const rows = (n: number) =>
  Array.from({ length: n }, (_, i) => ({ id: i, name: "n" + i, ok: i % 2 == 0, score: i * 1.5, note: i % 3 ? null : "é€😀" }))
docs.flat = rows(50)
docs.single = rows(1)
docs.empty = [{}, {}, { a: 1 }, {}]
docs.nested = Array.from({ length: 30 }, (_, i) => ({
  id: i,
  customer: i % 4 ? { id: i % 5, name: "c" + (i % 5) } : null,
  items: Array.from({ length: i % 5 }, (_, j) => ({ sku: "s" + j, qty: j, price: j * 1.25 })),
}))
docs.keyOrder = [{ a: 1, b: 2 }, { b: 2, a: 1 }, { a: 1, b: 2, c: 3 }, { a: 1, b: 2 }]
docs.inMap = { rows: rows(5), more: { rows: rows(3) } }
docs.dictionary = Array.from({ length: 20 }, (_, i) => ({ id: i, meta: { ["k" + i]: i, same: "x" } }))
// more shapes than cbor-x keeps ids for : it starts reusing ids, which means redefining them
docs.manyShapes = Array.from({ length: 700 }, (_, i) => ({ ["f" + i]: i, common: true }))
docs.wide = [Object.fromEntries(Array.from({ length: 300 }, (_, i) => ["c" + i, i])), Object.fromEntries(Array.from({ length: 300 }, (_, i) => ["c" + i, -i]))]
docs.deepArrays = [[[{ a: 1 }, { a: 2 }], [{ a: 3 }]], [[{ a: 4 }]]]
docs.mixed = [1, "x", { a: 1 }, [{ a: 2 }, { a: 3 }], null, { a: 4 }]

const encoder = new Encoder({ useRecords: true, tagUint8Array: false })
const decoder = new Decoder({ useRecords: false, mapsAsObjects: true })
for (const [name, doc] of Object.entries(docs)) {
  const bytes = encoder.encode(doc)
  console.log(JSON.stringify({ name, hex: Buffer.from(bytes).toString("hex"), json: JSON.stringify(decoder.decode(bytes)) }))
}
