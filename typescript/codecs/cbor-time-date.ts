// `codec=cbor,date` : tag 1 (an instant) is a `Date`, and a date (tag 100) stays the `YYYY-MM-DD` text JSON gives. A
// `Date` holds milliseconds : the microseconds of a timestamp are lost on read.
// docs/content/typescript/index.md ## Codecs.
import { pgWireCasts, requireOffset } from "../pg_values"
import { defineCborTag } from "./wire-cbor"

// tag 1 : seconds since the epoch, an integer or a float with microseconds
defineCborTag(1, {
  Class: Date,
  encode(value: Date, encodeValue: (v: unknown) => void) {
    encodeValue(value.getTime() / 1000)
  },
  decode(seconds: number) {
    return new Date(Math.round(seconds * 1000))
  },
})

// tag 100 : days since 1970-01-01, read as the YYYY-MM-DD text JSON gives (Date does not reach every year a date does)
defineCborTag(100, {
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

// What the write-side casts build. A date is the text JSON would carry, so its cast stays as it is.
pgWireCasts.timestamptz = (value: Date | string) => {
  if (value instanceof Date) return value
  requireOffset(value)
  return new Date(value)
}
pgWireCasts.timestamp = (value: Date) => {
  if (!(value instanceof Date)) throw new Error("a timestamp is written as a Date")
  return value
}
