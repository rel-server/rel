// `codec=date` : a timestamp is a `Date`. A `Date` holds milliseconds, so the microseconds of a timestamp are lost
// when it is read through an accessor (and on the wire, with `cbor`). A Postgres `date` stays the `YYYY-MM-DD` text,
// since a `Date` does not reach every year a `date` does : `temporal` gives a `Temporal.PlainDate`.
// docs/content/typescript/index.md ## Codecs.
import {
  type GetSetDescriptor,
  type PgDate,
  type Timestamp,
  type Timestamptz,
  pgWireCasts,
  requireOffset,
} from "../pg_values"

const isoOf = (value: Date | string): string =>
  typeof value === "string" ? value : value.toISOString()

// What JSON carries : the ISO text. `cbor` replaces these (codecs/cbor-time-date.ts).
pgWireCasts.timestamptz = (value: Date | string) => requireOffset(isoOf(value))
pgWireCasts.timestamp = (value: Date | string) => isoOf(value)
pgWireCasts.date = (value: string) => value

/** Brands a `Date` (or a timestamp text with a UTC offset) as a `Timestamptz`. */
export function asTimestamptz(value: Date | string): Timestamptz {
  return pgWireCasts.timestamptz(value) as Timestamptz
}

/**
 Brands a `Date` as a `Timestamp`. A `Date` is an instant : what the column keeps is that instant's wall clock in the
 session's time zone, and in UTC through JSON.
 */
export function asTimestamp(value: Date | string): Timestamp {
  return pgWireCasts.timestamp(value) as Timestamp
}

/** Brands a `YYYY-MM-DD` text as a `PgDate`. */
export function asPgDate(value: string): PgDate {
  return pgWireCasts.date(value) as PgDate
}

// A wall clock without an offset is read as UTC.
const toDate = (v: unknown): Date => {
  if (v instanceof Date) return v
  const text = v as string
  return new Date(/[Zz]$|[+-]\d{2}(:\d{2})?$/.test(text) ? text : `${text}Z`)
}

export function timestampTzAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamptz>): Date {
        return toDate(this[column])
      },
      set(this: Record<Col, Timestamptz>, value: Date | string) {
        this[column] = asTimestamptz(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_date`]: GetSetDescriptor<Date, Date | string> }
}

export function timestampAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamp>): Date {
        return toDate(this[column])
      },
      set(this: Record<Col, Timestamp>, value: Date | string) {
        this[column] = asTimestamp(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_date`]: GetSetDescriptor<Date, Date | string> }
}
