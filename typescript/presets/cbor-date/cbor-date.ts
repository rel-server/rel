// CBOR codec preset without Temporal : an instant is a `Date`, a date stays the text JSON gives. Copy this file next to
// your generated database.ts, fix the import below to its path, and import it once, before the first query :
// `import "./cbor-date"`. It needs `cbor-x` (bun add cbor-x). docs/content/typescript/index.md ## Codecs.
//
// The file has two halves that must stay together : the `PgWire` block says what the decoded values are, and the code
// below builds exactly those values (tags 1 and 37 decoded, `pgWireCasts` for writes). A `Date` holds milliseconds :
// the microseconds of a timestamp are lost on read. The `_as_date` accessors of a timestamptz column still return a
// Temporal.Instant and so need Temporal when used ; the ones of a timestamp and a date column pass through.
import { Decoder, Encoder, addExtension as addTypedExtension } from "cbor-x"
import { Querier, pgWireCasts } from "./database"

declare module "./database" {
  interface PgWire {
    // 8-byte integers (>= 2^32) are decoded as bigint by cbor-x, smaller ones are numbers
    int8: number | bigint
    timestamptz: Date
    // The server answers a timestamp as the instant its wall clock stands for in the connection's time zone : there
    // is no wall clock to read here, and the zone is only known to the server.
    timestamp: Date
    bytea: Uint8Array
  }
}

// cbor-x types an extension's encode as returning the bytes, but it writes them through the function it is given and
// ignores what is returned.
const addExtension = addTypedExtension as (extension: object) => void

// tag 1 : seconds since the epoch, an integer or a float with microseconds
addExtension({
  tag: 1,
  Class: Date,
  encode(value: Date, encodeValue: (v: unknown) => void) {
    encodeValue(value.getTime() / 1000)
  },
  decode(seconds: number) {
    return new Date(Math.round(seconds * 1000))
  },
})

// tag 100 : days since 1970-01-01, read as the YYYY-MM-DD text JSON gives (Date does not reach every year a date does)
addExtension({
  tag: 100,
  Class: class Day {},
  encode() {
    throw new Error("a date is written as text")
  },
  decode(days: number) {
    // days to a proleptic Gregorian calendar date (Howard Hinnant's civil_from_days)
    const z = days + 719468
    const era = Math.floor(z / 146097)
    const doe = z - era * 146097
    const yoe = Math.floor(
      (doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365,
    )
    const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100))
    const mp = Math.floor((5 * doy + 2) / 153)
    const day = doy - Math.floor((153 * mp + 2) / 5) + 1
    const month = mp < 10 ? mp + 3 : mp - 9
    const year = yoe + era * 400 + (month <= 2 ? 1 : 0)
    const p = (n: number, w: number) => String(n).padStart(w, "0")
    // Postgres spells a year before 1 as its BC year
    return `${p(year >= 1 ? year : 1 - year, 4)}-${p(month, 2)}-${p(day, 2)}${year >= 1 ? "" : " BC"}`
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

// What the write-side casts build. A date is the text JSON would carry, so its cast stays as it is.
const castTimestamptz = pgWireCasts.timestamptz
pgWireCasts.timestamptz = (value) => {
  if (value instanceof Date) return value
  castTimestamptz(value) // refuses a string without an offset
  return new Date(value as string)
}
pgWireCasts.timestamp = (value) => {
  if (!(value instanceof Date)) throw new Error("a timestamp is written as a Date")
  return value
}
pgWireCasts.bytea = (value) => {
  if (typeof value === "string") throw new Error("a bytea is written as a Uint8Array")
  return value
}
