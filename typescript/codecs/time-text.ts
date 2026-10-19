// `codec=` without a time token : a timestamp or a date is the text JSON carries, written back as the same text.
// No accessor is offered, so nothing here needs `Temporal`. docs/content/typescript/index.md ## Codecs.
import {
  type PgDate,
  type Timestamp,
  type Timestamptz,
  pgWireCasts,
  requireOffset,
} from "../pg_values"

pgWireCasts.timestamptz = (value: string) => requireOffset(value)
pgWireCasts.timestamp = (value: string) => value
pgWireCasts.date = (value: string) => value

/** Brands a timestamp text as a `Timestamptz`; throws unless it carries a UTC offset. */
export function asTimestamptz(value: string): Timestamptz {
  return pgWireCasts.timestamptz(value) as Timestamptz
}

/** Brands a wall-clock text as a `Timestamp`. */
export function asTimestamp(value: string): Timestamp {
  return pgWireCasts.timestamp(value) as Timestamp
}

/** Brands a `YYYY-MM-DD` text as a `PgDate`. */
export function asPgDate(value: string): PgDate {
  return pgWireCasts.date(value) as PgDate
}
