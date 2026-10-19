// `codec=cbor` : requests and responses are CBOR, which carries 8-byte integers (read as `number | bigint`) and bytes
// natively. It needs `cbor-x` (bun add cbor-x). The date and time tags are filled in by the time chunk
// (codecs/cbor-time-*.ts) through `defineCborTag`. docs/content/typescript/index.md ## Codecs.
import { Decoder, Encoder, addExtension } from "cbor-x"
import { Querier } from "../querier"
import { pgWireCasts } from "../pg_values"

/** What one CBOR tag is on this side : the class it is written from, and how an item is read and written. */
export interface CborTag<T = unknown> {
  Class: new (...args: never[]) => T
  encode(value: T, encodeValue: (v: unknown) => void): void
  decode(item: never): T
}

/**
 The tags this client reads and writes, by tag number. Replace an entry (or one of its functions) to change what a
 tag becomes ; the codec looks the entry up at each use, so it works after the first query too. The value must keep
 the type `PgWire` says.
 */
export const cborTags: Record<number, CborTag> = {}

/** Registers a tag with cbor-x (a registry shared by the whole program) and puts it in `cborTags`. */
export function defineCborTag<T>(tag: number, handler: CborTag<T>): void {
  cborTags[tag] = handler as CborTag
  // cbor-x types an extension's encode as returning the bytes, but it writes them through the function it is
  // given and ignores what is returned.
  ;(addExtension as (extension: object) => void)({
    tag,
    Class: handler.Class,
    encode: (value: unknown, encodeValue: (v: unknown) => void) =>
      cborTags[tag]?.encode(value, encodeValue),
    decode: (item: never) => cborTags[tag]?.decode(item),
  })
}

// tag 37 : a UUID as 16 bytes, read as the hyphenated text JSON gives. Never written : a uuid column takes its text.
defineCborTag(37, {
  Class: class Uuid {},
  encode() {
    throw new Error("a uuid is written as text")
  },
  decode(bytes: Uint8Array) {
    const h = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
  },
} as CborTag)

// a Uint8Array is a plain byte string : cbor-x would tag it 64 (RFC 8746) otherwise
const options = { useRecords: false, mapsAsObjects: true, tagUint8Array: false }
const decoder = new Decoder(options)
const encoder = new Encoder(options)
// Structures : objects with the same keys are written as the keys once, then the values (specs/cbor.md ## Structures).
// Never `sequential`, which refers to structures defined by an earlier body : the server reads each request alone.
// The decoder needs no option to read them.
// `mapsAsObjects` stays unset : with it, cbor-x would tag every `Map` it writes with 259.
const structuredEncoder = new Encoder({ useRecords: true, tagUint8Array: false })

// cbor-x gives two values the server refuses what JSON would have carried : `undefined` (JSON drops an object member
// that is undefined, and turns an array item into null), and a whole number from 2^32 up, which it writes as a
// float where the server wants an integer for an int8 column (an integer is fine for a float column). The value is
// rewritten as JSON would read it, with those numbers as bigints.
function forServer(v: unknown): unknown {
  if (typeof v === "number") return Number.isInteger(v) && Math.abs(v) >= 2 ** 32 ? BigInt(v) : v
  if (Array.isArray(v)) return v.map((x) => (x === undefined ? null : forServer(x)))
  if (v !== null && typeof v === "object" && Object.getPrototypeOf(v) === Object.prototype) {
    const out: Record<string, unknown> = {}
    for (const [k, x] of Object.entries(v)) {
      if (x !== undefined) out[k] = forServer(x)
    }
    return out
  }
  return v
}

Querier.prototype.codec = {
  contentType: "application/cbor",
  // Responses use structures too. cbor-x reads them fastest by compiling a reader per layout with `new Function`; where
  // that is refused (a Content-Security-Policy without 'unsafe-eval') it notices at load, once, and reads them without
  // compiling, still faster than maps. That one refusal is reported to the policy, though : import `cbor-x/index-no-eval`
  // instead of `cbor-x` to avoid it. docs/content/typescript/index.md ### Structures.
  accept: "application/cbor; structures=1",
  binary: true,
  encode(value, opts) {
    const enc = opts?.structures === false ? encoder : structuredEncoder
    const bytes = enc.encode(value)
    // 0xf7 is `undefined` and 0xfb a double : both can also sit inside a string, a byte string or a number's
    // bytes, which only costs the second pass
    return bytes.includes(0xf7) || bytes.includes(0xfb) ? enc.encode(forServer(value)) : bytes
  },
  decode: (body) => decoder.decode(body as Uint8Array),
}

pgWireCasts.bytea = (value: Uint8Array | string) => {
  if (typeof value === "string") throw new Error("a bytea is written as a Uint8Array")
  return value
}
