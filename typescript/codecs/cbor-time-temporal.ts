// `codec=cbor,temporal` : tag 1 (an instant) is a `Temporal.Instant` and tag 100 (a date) a `Temporal.PlainDate`.
// Needs a runtime with `Temporal`, or a polyfill installed before the first query.
// docs/content/typescript/index.md ## Codecs.
import { pgWireCasts } from "../pg_values"
import { defineCborTag } from "./wire-cbor"

const epochDay = Temporal.PlainDate.from("1970-01-01")

// tag 1 : seconds since the epoch, an integer or a float with microseconds
defineCborTag(1, {
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
defineCborTag(100, {
  Class: Temporal.PlainDate,
  encode(value: Temporal.PlainDate, encodeValue: (v: unknown) => void) {
    encodeValue(epochDay.until(value, { largestUnit: "days" }).days)
  },
  decode(days: number) {
    return epochDay.add({ days })
  },
})

// What the write-side casts build. A string must carry what a Temporal parse needs (an offset for an instant).
pgWireCasts.timestamptz = (value: Temporal.Instant | Date | string) =>
  value instanceof Temporal.Instant
    ? value
    : value instanceof Date
      ? Temporal.Instant.fromEpochMilliseconds(value.getTime())
      : Temporal.Instant.from(value)
pgWireCasts.timestamp = (value: Temporal.Instant | string) =>
  value instanceof Temporal.Instant ? value : Temporal.Instant.from(value)
pgWireCasts.date = (value: Temporal.PlainDate | string) =>
  typeof value === "string" ? Temporal.PlainDate.from(value) : value
