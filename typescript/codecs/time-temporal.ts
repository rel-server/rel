// `codec=temporal` : a timestamp is a `Temporal.Instant` (or a `PlainDateTime` for a wall clock), a `date` a
// `Temporal.PlainDate`, and an interval or a date range gets its accessor. Needs a runtime with `Temporal`, or a
// polyfill installed before the first query. docs/content/typescript/index.md ## Codecs.
import {
  type DateRange,
  type GetOnlyDescriptor,
  type GetSetDescriptor,
  type Interval,
  type PgDate,
  type PgWireType,
  Range,
  type Timestamp,
  type Timestamptz,
  type TsRange,
  type TstzRange,
  asInterval,
  pgWireCasts,
  requireOffset,
} from "../pg_values"

const isoOf = (value: Date | Temporal.Instant | Temporal.PlainDateTime | string): string =>
  typeof value === "string" ? value : value instanceof Date ? value.toISOString() : value.toString()

// What JSON carries : the ISO text. `cbor` replaces these (codecs/cbor-time-temporal.ts).
pgWireCasts.timestamptz = (value: Date | Temporal.Instant | string) => requireOffset(isoOf(value))
pgWireCasts.timestamp = (value: Temporal.PlainDateTime | Temporal.Instant | Date | string) =>
  isoOf(value)
pgWireCasts.date = (value: Temporal.PlainDate | string) =>
  typeof value === "string" ? value : value.toString()

/** Brands an instant (or a timestamp text with a UTC offset) as a `Timestamptz`. */
export function asTimestamptz(value: Date | Temporal.Instant | string): Timestamptz {
  return pgWireCasts.timestamptz(value) as Timestamptz
}

/** What a `Timestamp` is read and written as : a wall clock through JSON, an instant through `cbor`. */
export type TimestampValue = PgWireType<"timestamp", Temporal.PlainDateTime>

/** Brands a wall clock as a `Timestamp`. Through `cbor` the server reads an instant, in its session's time zone. */
export function asTimestamp(value: TimestampValue | string): Timestamp {
  return pgWireCasts.timestamp(value) as Timestamp
}

/** Brands a `Temporal.PlainDate` (or a `YYYY-MM-DD` text) as a `PgDate`. */
export function asPgDate(value: Temporal.PlainDate | string): PgDate {
  return pgWireCasts.date(value) as PgDate
}

// The wire value of a date or time type is a string through JSON and already the Temporal value through `cbor` : an
// accessor converts what it is given and passes through what already is the richer value.
function toInstant(v: unknown): Temporal.Instant {
  if (v instanceof Temporal.Instant) return v
  if (v instanceof Date) return Temporal.Instant.fromEpochMilliseconds(v.getTime())
  return Temporal.Instant.from(v as string)
}

export function timestampTzAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamptz>): Temporal.Instant {
        return toInstant(this[column])
      },
      set(this: Record<Col, Timestamptz>, value: Date | Temporal.Instant | string) {
        this[column] = asTimestamptz(value)
      },
      enumerable: true,
    },
  } as {
    [K in `${Col}_as_date`]: GetSetDescriptor<Temporal.Instant, Date | Temporal.Instant | string>
  }
}

export function timestampAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamp>): TimestampValue {
        const v: unknown = this[column]
        // a wall clock out of text ; an instant (PgWire["timestamp"] retyped by the codec) is already the value
        return (typeof v === "string" ? Temporal.PlainDateTime.from(v) : v) as TimestampValue
      },
      set(this: Record<Col, Timestamp>, value: TimestampValue | string) {
        this[column] = asTimestamp(value)
      },
      enumerable: true,
    },
  } as {
    [K in `${Col}_as_date`]: GetSetDescriptor<TimestampValue, TimestampValue | string>
  }
}

export function dateAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, PgDate>): Temporal.PlainDate {
        const v: unknown = this[column]
        return (typeof v === "string" ? Temporal.PlainDate.from(v) : v) as Temporal.PlainDate
      },
      set(this: Record<Col, PgDate>, value: Temporal.PlainDate | string) {
        this[column] = asPgDate(value)
      },
      enumerable: true,
    },
  } as {
    [K in `${Col}_as_date`]: GetSetDescriptor<Temporal.PlainDate, Temporal.PlainDate | string>
  }
}

// Assumes IntervalStyle = postgres (Postgres's own default) and throws on a string it doesn't recognize, rather
// than silently misinterpreting a value produced under a different style. Sub-millisecond precision (a 4th+
// fractional-second digit) is rounded to the nearest millisecond — Temporal.Duration's own millisecond field is
// the finest granularity used here, not a fundamental limit of the format itself.
function parseInterval(text: string): Temporal.Duration {
  let years = 0
  let months = 0
  let days = 0
  let hours = 0
  let minutes = 0
  let seconds = 0
  let milliseconds = 0
  let rest = text

  const yearMatch = /(-?\d+)\s+years?/.exec(rest)
  if (yearMatch) {
    years = Number(yearMatch[1])
    rest = rest.replace(yearMatch[0], "").trim()
  }
  const monMatch = /(-?\d+)\s+mons?/.exec(rest)
  if (monMatch) {
    months = Number(monMatch[1])
    rest = rest.replace(monMatch[0], "").trim()
  }
  const dayMatch = /(-?\d+)\s+days?/.exec(rest)
  if (dayMatch) {
    days = Number(dayMatch[1])
    rest = rest.replace(dayMatch[0], "").trim()
  }

  if (rest) {
    const timeMatch = /^(-?)(\d+):(\d+):(\d+)(\.\d+)?$/.exec(rest)
    if (!timeMatch) {
      throw new Error(
        `parseInterval: "${text}" doesn't match the expected 'postgres' IntervalStyle format`,
      )
    }
    const sign = timeMatch[1] === "-" ? -1 : 1
    hours = sign * Number(timeMatch[2])
    minutes = sign * Number(timeMatch[3])
    seconds = sign * Number(timeMatch[4])
    if (timeMatch[5]) {
      milliseconds = sign * Math.round(Number(timeMatch[5]) * 1000)
    }
  } else if (!yearMatch && !monMatch && !dayMatch) {
    throw new Error(
      `parseInterval: "${text}" doesn't match the expected 'postgres' IntervalStyle format`,
    )
  }

  return Temporal.Duration.from({ years, months, days, hours, minutes, seconds, milliseconds })
}

export function intervalAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_duration`]: {
      get(this: Record<Col, Interval>): Temporal.Duration {
        return parseInterval(this[column])
      },
      set(this: Record<Col, Interval>, value: Temporal.Duration | string) {
        this[column] = asInterval(typeof value === "string" ? value : value.toString())
      },
      enumerable: true,
    },
  } as {
    [K in `${Col}_as_duration`]: GetSetDescriptor<Temporal.Duration, Temporal.Duration | string>
  }
}

export function dateRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, DateRange>): Range<Temporal.PlainDate> {
        return Range.parse(
          this[column],
          (s) => Temporal.PlainDate.from(s),
          Temporal.PlainDate.compare,
        )
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<Temporal.PlainDate>> }
}

export function tsRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, TsRange>): Range<Temporal.PlainDateTime> {
        return Range.parse(
          this[column],
          (s) => Temporal.PlainDateTime.from(s),
          Temporal.PlainDateTime.compare,
        )
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<Temporal.PlainDateTime>> }
}

export function tstzRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, TstzRange>): Range<Temporal.Instant> {
        return Range.parse(this[column], (s) => Temporal.Instant.from(s), Temporal.Instant.compare)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<Temporal.Instant>> }
}
