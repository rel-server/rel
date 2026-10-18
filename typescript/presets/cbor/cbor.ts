// CBOR codec preset. Copy this file next to your generated database.ts, fix the import below to its path, and import
// it once, before the first query : `import "./cbor"`. It needs `cbor-x` (bun add cbor-x) and a runtime with
// `Temporal` (or a polyfill installed before this file runs). docs/content/typescript/index.md ## Codecs.
//
// The file has two halves that must stay together : the `PgWire` block says what the decoded values are, and the code
// below builds exactly those values (tags 1, 100 and 37 decoded, `pgWireCasts` for writes).
import { Decoder, Encoder, addExtension as addTypedExtension } from "cbor-x"
import { Querier, pgWireCasts } from "./database"

declare module "./database" {
  interface PgWire {
    // 8-byte integers (>= 2^32) are decoded as bigint by cbor-x, smaller ones are numbers
    int8: number | bigint
    timestamptz: Temporal.Instant
    // The server answers a timestamp as the instant its wall clock stands for in the connection's time zone : there
    // is no wall clock to read here, and the zone is only known to the server.
    timestamp: Temporal.Instant
    date: Temporal.PlainDate
    bytea: Uint8Array
  }
}

// cbor-x types an extension's encode as returning the bytes, but it writes them through the function it is given and
// ignores what is returned.
const addExtension = addTypedExtension as (extension: object) => void

const epochDay = Temporal.PlainDate.from("1970-01-01")

// tag 1 : seconds since the epoch, an integer or a float with microseconds
addExtension({
  tag: 1,
  Class: Temporal.Instant,
  encode(value: Temporal.Instant, encodeValue: (v: unknown) => void) {
    const micros = value.epochNanoseconds / 1000n
    encodeValue(micros % 1000000n === 0n ? Number(micros / 1000000n) : Number(micros) / 1e6)
  },
  decode(seconds: number) {
    return Temporal.Instant.fromEpochNanoseconds(BigInt(Math.round(seconds * 1e6)) * 1000n)
  },
})

// tag 100 : days since 1970-01-01
addExtension({
  tag: 100,
  Class: Temporal.PlainDate,
  encode(value: Temporal.PlainDate, encodeValue: (v: unknown) => void) {
    encodeValue(epochDay.until(value, { largestUnit: "days" }).days)
  },
  decode(days: number) {
    return epochDay.add({ days })
  },
})

// tag 37 : a UUID as 16 bytes, read as the hyphenated text JSON gives. Never written : a uuid column takes its text.
addExtension({
  tag: 37,
  Class: class Uuid {},
  encode() {
    throw new Error("a uuid is written as text")
  },
  decode(bytes: Uint8Array) {
    const h = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
  },
})

// a Uint8Array is a plain byte string : cbor-x would tag it 64 (RFC 8746) otherwise
const options = { useRecords: false, mapsAsObjects: true, tagUint8Array: false }
const encoder = new Encoder(options)
const decoder = new Decoder(options)

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
  binary: true,
  encode(value) {
    const bytes = encoder.encode(value)
    // 0xf7 is `undefined` and 0xfb a double : both can also sit inside a string, a byte string or a number's
    // bytes, which only costs the second pass
    return bytes.includes(0xf7) || bytes.includes(0xfb) ? encoder.encode(forServer(value)) : bytes
  },
  decode: (body) => decoder.decode(body as Uint8Array),
}

// What the write-side casts build. A string must carry what a Temporal parse needs (an offset for an instant).
pgWireCasts.timestamptz = (value) =>
  value instanceof Temporal.Instant
    ? value
    : value instanceof Date
      ? Temporal.Instant.fromEpochMilliseconds(value.getTime())
      : Temporal.Instant.from(value)
pgWireCasts.timestamp = (value) =>
  value instanceof Temporal.Instant ? value : Temporal.Instant.from(value as string)
pgWireCasts.date = (value) => (typeof value === "string" ? Temporal.PlainDate.from(value) : value)
pgWireCasts.bytea = (value) => {
  if (typeof value === "string") throw new Error("a bytea is written as a Uint8Array")
  return value
}
