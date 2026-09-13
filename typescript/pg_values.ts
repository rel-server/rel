/*!
specs/typescript-wire-types.md : branded string types for every Postgres type whose JSON wire representation
isn't the JS type it might look like, plus the write-side casts and read-side accessors for deriving a richer
client-side value from one, without losing type safety or requiring a runtime dependency. Concatenated first
(tsgen/embed.go), so the generated schema section and every other section can reference these names directly.
*/

// ---- Temporal-backed branded types (## Corrected type mapping, ## Read-side conversions) ----

export type Timestamptz = string & { readonly __pg: "timestamptz" }
export type Timestamp = string & { readonly __pg: "timestamp" }
export type PgDate = string & { readonly __pg: "date" }
export type PgTime = string & { readonly __pg: "time" }
export type PgTimetz = string & { readonly __pg: "timetz" }
export type Interval = string & { readonly __pg: "interval" }

// ---- Precision-preserving branded types (## Corrected type mapping) ----

export type Money = string & { readonly __pg: "money" }
export type Int8 = string & { readonly __pg: "int8" }
export type PgNumeric = string & { readonly __pg: "numeric" }

// ---- Range types (## Range types) ----

export type Int4Range = string & { readonly __pg: "int4range" }
export type Int8Range = string & { readonly __pg: "int8range" }
export type NumRange = string & { readonly __pg: "numrange" }
export type DateRange = string & { readonly __pg: "daterange" }
export type TsRange = string & { readonly __pg: "tsrange" }
export type TstzRange = string & { readonly __pg: "tstzrange" }

// ---- Multirange types (## Multiranges) ----

export type Int4MultiRange = string & { readonly __pg: "int4multirange" }
export type Int8MultiRange = string & { readonly __pg: "int8multirange" }
export type NumMultiRange = string & { readonly __pg: "nummultirange" }
export type DateMultiRange = string & { readonly __pg: "datemultirange" }
export type TsMultiRange = string & { readonly __pg: "tsmultirange" }
export type TstzMultiRange = string & { readonly __pg: "tstzmultirange" }

// ---- Other branded string types (## Other types) — no accessor written out for any of these yet, see that
// section's own table for which ones a future accessor would even make sense for. ----

export type Inet = string & { readonly __pg: "inet" }
export type Cidr = string & { readonly __pg: "cidr" }
export type MacAddr = string & { readonly __pg: "macaddr" }
export type MacAddr8 = string & { readonly __pg: "macaddr8" }
export type PgBit = string & { readonly __pg: "bit" }
export type PgVarbit = string & { readonly __pg: "varbit" }
export type PgPoint = string & { readonly __pg: "point" }
export type PgLine = string & { readonly __pg: "line" }
export type PgLseg = string & { readonly __pg: "lseg" }
export type PgBox = string & { readonly __pg: "box" }
export type PgPath = string & { readonly __pg: "path" }
export type PgPolygon = string & { readonly __pg: "polygon" }
export type PgCircle = string & { readonly __pg: "circle" }
export type TsVector = string & { readonly __pg: "tsvector" }
export type TsQuery = string & { readonly __pg: "tsquery" }
export type Xml = string & { readonly __pg: "xml" }
export type PgLsn = string & { readonly __pg: "pg_lsn" }

// ---- Write-side casts (## Write-side casts) ----

// asTimestamptz requires a trailing `Z` or `±HH:MM`/`±HH` offset — the one type in this file with a
// meaningful runtime check, since Postgres would otherwise silently reinterpret an offset-less string using
// the session's own time zone.
export function asTimestamptz(value: Date | Temporal.Instant | string): Timestamptz {
  const s = typeof value === "string" ? value : value.toString()
  if (!/[Zz]$|[+-]\d{2}(:\d{2})?$/.test(s)) {
    throw new Error(`asTimestamptz: "${s}" has no time zone offset`)
  }
  return s as Timestamptz
}

// Round-tripping a Temporal.PlainDateTime back to a Timestamp is lossless and unambiguous — PlainDateTime never
// invented a time-zone assumption in the first place.
export function asTimestamp(value: Temporal.PlainDateTime | string): Timestamp {
  return (typeof value === "string" ? value : value.toString()) as Timestamp
}

export function asPgDate(value: Temporal.PlainDate | string): PgDate {
  return (typeof value === "string" ? value : value.toString()) as PgDate
}

export function asMoney(value: string): Money {
  return value as Money
}

// bigint/number are always valid ; a string is passed through as-is, with no client-side re-validation, since
// Postgres's own int8 input parser already rejects a malformed one on write.
export function asInt8(value: bigint | number | string): Int8 {
  return String(value) as Int8
}

export function asNumeric(value: number | string): PgNumeric {
  return String(value) as PgNumeric
}

export function asInterval(value: Temporal.Duration | string): Interval {
  return (typeof value === "string" ? value : value.toString()) as Interval
}

// ---- Comparators (## Range values) ----

export function compareBigInt(a: bigint, b: bigint): number {
  return a < b ? -1 : a > b ? 1 : 0
}

// Exact decimal-string comparison, no Number() conversion — comparing (not computing on) two arbitrary-scale
// decimals doesn't need a full decimal library, just digit-string comparison. NaN sorts last, Postgres's own
// convention for its numeric NaN value.
export function compareNumeric(a: PgNumeric, b: PgNumeric): number {
  if (a === "NaN" || b === "NaN") return a === b ? 0 : a === "NaN" ? 1 : -1
  const negA = a.startsWith("-")
  const negB = b.startsWith("-")
  if (negA !== negB) return negA ? -1 : 1
  const sign = negA ? -1 : 1
  const restA = negA ? a.slice(1) : a
  const restB = negB ? b.slice(1) : b
  const [intA, fracA = ""] = restA.split(".")
  const [intB, fracB = ""] = restB.split(".")
  const ti = intA.replace(/^0+(?=\d)/, "")
  const tj = intB.replace(/^0+(?=\d)/, "")
  if (ti.length !== tj.length) return sign * (ti.length > tj.length ? 1 : -1)
  if (ti !== tj) return sign * (ti > tj ? 1 : -1)
  const maxLen = Math.max(fracA.length, fracB.length)
  const fa = fracA.padEnd(maxLen, "0")
  const fb = fracB.padEnd(maxLen, "0")
  return fa === fb ? 0 : sign * (fa > fb ? 1 : -1)
}

// Postgres quotes a range bound's own text when it would otherwise be ambiguous (tsrange/tstzrange's bounds,
// which contain a space) ; strips that quoting and un-escapes \" / \\ before parseBound ever sees it.
function unquoteBound(raw: string): string {
  if (raw.length >= 2 && raw[0] === '"' && raw[raw.length - 1] === '"') {
    return raw.slice(1, -1).replace(/\\(.)/g, "$1")
  }
  return raw
}

// ---- Range<T> (## Range values) : one shared, immutable class ; each range kind is Range<T> for whichever T
// its own accessor parses bounds into (Range<number> for Int4Range, Range<bigint> for Int8Range, Range<PgNumeric>
// for NumRange — left as branded strings, not parsed further — Range<Temporal.PlainDate|PlainDateTime|Instant>
// for the temporal ranges). ----

export class Range<T> {
  private constructor(
    readonly lower: T | null,
    readonly upper: T | null,
    readonly lowerInclusive: boolean,
    readonly upperInclusive: boolean,
    readonly isEmpty: boolean,
    private readonly compare: (a: T, b: T) => number,
  ) {}

  static parse<T>(
    text: string,
    parseBound: (raw: string) => T,
    compare: (a: T, b: T) => number,
  ): Range<T> {
    if (text === "empty") {
      return new Range<T>(null, null, false, false, true, compare)
    }
    const m = /^([[(])([^,]*),(.*)([\])])$/.exec(text)
    if (!m) {
      throw new Error(`Range.parse: "${text}" is not a valid range`)
    }
    const [, lb, lowerRaw, upperRaw, ub] = m
    return new Range<T>(
      lowerRaw ? parseBound(unquoteBound(lowerRaw)) : null,
      upperRaw ? parseBound(unquoteBound(upperRaw)) : null,
      lb === "[",
      ub === "]",
      false,
      compare,
    )
  }

  contains(value: T): boolean {
    if (this.isEmpty) return false
    if (this.lower !== null) {
      const c = this.compare(value, this.lower)
      if (c < 0 || (c === 0 && !this.lowerInclusive)) return false
    }
    if (this.upper !== null) {
      const c = this.compare(value, this.upper)
      if (c > 0 || (c === 0 && !this.upperInclusive)) return false
    }
    return true
  }

  overlaps(other: Range<T>): boolean {
    if (this.isEmpty || other.isEmpty) return false
    const thisEndsBeforeOther =
      this.upper !== null &&
      other.lower !== null &&
      (this.compare(this.upper, other.lower) < 0 ||
        (this.compare(this.upper, other.lower) === 0 &&
          !(this.upperInclusive && other.lowerInclusive)))
    const otherEndsBeforeThis =
      other.upper !== null &&
      this.lower !== null &&
      (this.compare(other.upper, this.lower) < 0 ||
        (this.compare(other.upper, this.lower) === 0 &&
          !(other.upperInclusive && this.lowerInclusive)))
    return !thisEndsBeforeOther && !otherEndsBeforeThis
  }

  equals(other: Range<T>): boolean {
    if (this.isEmpty || other.isEmpty) return this.isEmpty === other.isEmpty
    const boundEqual = (a: T | null, b: T | null): boolean =>
      a === null || b === null ? a === b : this.compare(a, b) === 0
    return (
      boundEqual(this.lower, other.lower) &&
      boundEqual(this.upper, other.upper) &&
      this.lowerInclusive === other.lowerInclusive &&
      this.upperInclusive === other.upperInclusive
    )
  }

  // Round-trips back to Postgres's own range text ; formatBound is the accessor's own inverse of parseBound.
  toString(formatBound: (v: T) => string): string {
    if (this.isEmpty) return "empty"
    const lb = this.lowerInclusive ? "[" : "("
    const ub = this.upperInclusive ? "]" : ")"
    const lo = this.lower === null ? "" : formatBound(this.lower)
    const hi = this.upper === null ? "" : formatBound(this.upper)
    return `${lb}${lo},${hi}${ub}`
  }
}

// ---- MultiRange<T> (## Multiranges) : `{` + comma-joined segments, each parsed the same way a single range's
// own accessor would. ----

function splitTopLevelCommas(text: string): string[] {
  const segments: string[] = []
  let depth = 0
  let start = 0
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (c === "[" || c === "(") depth++
    else if (c === "]" || c === ")") depth--
    else if (c === "," && depth === 0) {
      segments.push(text.slice(start, i))
      start = i + 1
    }
  }
  segments.push(text.slice(start))
  return segments
}

export class MultiRange<T> {
  private constructor(readonly ranges: readonly Range<T>[]) {}

  static parse<T>(
    text: string,
    parseBound: (raw: string) => T,
    compare: (a: T, b: T) => number,
  ): MultiRange<T> {
    const inner = text.slice(1, -1)
    if (inner === "") {
      return new MultiRange<T>([])
    }
    return new MultiRange<T>(
      splitTopLevelCommas(inner).map((seg) => Range.parse(seg, parseBound, compare)),
    )
  }

  contains(value: T): boolean {
    return this.ranges.some((r) => r.contains(value))
  }
}

// ---- Accessor helpers (## Accessor helpers, ## Read-side conversions) : each is a function taking a column
// name and returning a one-entry descriptor map keyed by a derived name, composed into `proto` via ordinary
// object spread. A helper's own get/set type `this` explicitly (ThisType doesn't apply outside the literal
// `proto` is checked against directly). ----

export function timestampTzAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamptz>): Temporal.Instant {
        return Temporal.Instant.from(this[column])
      },
      set(this: Record<Col, Timestamptz>, value: Date | Temporal.Instant | string) {
        this[column] = asTimestamptz(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_date`]: TypedPropertyDescriptor<Temporal.Instant> }
}

export function timestampAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, Timestamp>): Temporal.PlainDateTime {
        return Temporal.PlainDateTime.from(this[column])
      },
      set(this: Record<Col, Timestamp>, value: Temporal.PlainDateTime | string) {
        this[column] = asTimestamp(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_date`]: TypedPropertyDescriptor<Temporal.PlainDateTime> }
}

export function dateAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_date`]: {
      get(this: Record<Col, PgDate>): Temporal.PlainDate {
        return Temporal.PlainDate.from(this[column])
      },
      set(this: Record<Col, PgDate>, value: Temporal.PlainDate | string) {
        this[column] = asPgDate(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_date`]: TypedPropertyDescriptor<Temporal.PlainDate> }
}

export function int8Accessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_bigint`]: {
      get(this: Record<Col, Int8>): bigint {
        return BigInt(this[column])
      },
      set(this: Record<Col, Int8>, value: bigint | number | string) {
        this[column] = asInt8(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_bigint`]: TypedPropertyDescriptor<bigint> }
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
        this[column] = asInterval(value)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_duration`]: TypedPropertyDescriptor<Temporal.Duration> }
}

// ---- Range accessors (## Range values) ----

export function int4RangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, Int4Range>): Range<number> {
        return Range.parse(this[column], Number, (a, b) => a - b)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<number>> }
}

export function int8RangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, Int8Range>): Range<bigint> {
        return Range.parse(this[column], BigInt, compareBigInt)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<bigint>> }
}

export function numRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, NumRange>): Range<PgNumeric> {
        return Range.parse(this[column], (s) => s as PgNumeric, compareNumeric)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<PgNumeric>> }
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
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<Temporal.PlainDate>> }
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
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<Temporal.PlainDateTime>> }
}

export function tstzRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, TstzRange>): Range<Temporal.Instant> {
        return Range.parse(this[column], (s) => Temporal.Instant.from(s), Temporal.Instant.compare)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<Temporal.Instant>> }
}

// ---- Point accessor (## Other types) : the one geometric type prioritized for a real accessor, since it's the
// simplest and most commonly used ; line/lseg/box/path/polygon/circle stay branded strings with no accessor,
// see that section's own Why. ----

export function pointAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_point`]: {
      get(this: Record<Col, PgPoint>): { x: number; y: number } {
        const m = /^\(([^,]+),([^)]+)\)$/.exec(this[column])
        if (!m) {
          throw new Error(`pointAccessor: "${this[column]}" is not a valid point`)
        }
        return { x: Number(m[1]), y: Number(m[2]) }
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_point`]: TypedPropertyDescriptor<{ x: number; y: number }> }
}
