/*!
docs/content/typescript/index.md ## Typed wire values : branded string types for every Postgres type whose JSON wire representation
isn't the JS type it might look like, plus the write-side casts and read-side accessors for deriving a richer
client-side value from one, without losing type safety or requiring a runtime dependency. Concatenated first
(tsgen/embed.go), so the generated schema section and every other section can reference these names directly.
*/

// ---- Temporal-backed branded types ----

export type Timestamptz = PgWireType<"timestamptz", string> & { readonly __pg: "timestamptz" }
export type Timestamp = PgWireType<"timestamp", string> & { readonly __pg: "timestamp" }
export type PgDate = PgWireType<"date", string> & { readonly __pg: "date" }
export type PgTime = string & { readonly __pg: "time" }
export type PgTimetz = string & { readonly __pg: "timetz" }
export type Interval = string & { readonly __pg: "interval" }

// ---- Types whose JS value depends on the codec : typed by what the codec in use yields ----

/**
 What the JS values of the Postgres types that a codec (`Querier.prototype.codec`) may hand over as something other
 than the JSON default actually are. It is empty on purpose, so client code can say what its codec produces, by
 declaration merging in a file of its own, once per project (presets/ has one per codec, runtime and type halves
 together) :

     declare module "./database" {
       interface PgWire {
         int8: bigint                     // a codec that reads every big integer as a bigint
         numeric: LosslessNumber
         timestamptz: Temporal.Instant    // CBOR : tag 1 ; timestamp, an instant in the server's zone, likewise
         date: Temporal.PlainDate         // CBOR : tag 100
         bytea: Uint8Array                // CBOR : a byte string
       }
     }

 Without it `Int8` and `PgNumeric` are `number`, which is what the browser's JSON.parse gives (and silently
 rounds anything beyond 2^53 or about 15 significant digits), and the date and time types and `bytea` are the
 strings JSON carries them as. The members cannot be redeclared with another type afterwards, which is why the
 defaults live in the types below and not here. The values the write-side casts build must match what a member
 says : see `pgWireCasts`.
 */
// biome-ignore lint/suspicious/noEmptyInterface: filled in by client code through declaration merging
export interface PgWire {}

/** The JS type a wire member of `PgWire` stands for : its augmented type, or `Default`. */
export type PgWireType<K extends string, Default> = PgWire extends { [P in K]: infer T }
  ? T
  : Default

/** `bytea` : the `\x…` hex text JSON gives, or whatever `PgWire["bytea"]` says (a `Uint8Array` with the CBOR preset). */
export type Bytea = PgWireType<"bytea", string>
export type Money = string & { readonly __pg: "money" }
export type Int8 = PgWireType<"int8", number> & { readonly __pg: "int8" }
export type PgNumeric = PgWireType<"numeric", number> & { readonly __pg: "numeric" }

// ---- Table-local id types ----

/**
 Id types of the serial/bigserial/identity columns (and of the foreign keys pointing at them), keyed by the raw
 "schema.table.column" of the serial/identity column. Declared empty here and filled in by the generated schema
 (tsgen/ids.go) : `Id["hotel.properties.id"]` is `number & { readonly __id: "hotel.properties.id" }`, or the
 same over `Int8` for a bigserial. An id assigns to a bare `number`/`Int8`, never the other way round, and never
 to another table's id : write `1 as Id["hotel.properties.id"]` for a literal.
 */
// biome-ignore lint/suspicious/noEmptyInterface: filled in by the generated schema through declaration merging
export interface Id {}

// ---- Range types ----

export type Int4Range = string & { readonly __pg: "int4range" }
export type Int8Range = string & { readonly __pg: "int8range" }
export type NumRange = string & { readonly __pg: "numrange" }
export type DateRange = string & { readonly __pg: "daterange" }
export type TsRange = string & { readonly __pg: "tsrange" }
export type TstzRange = string & { readonly __pg: "tstzrange" }

// ---- Multirange types ----

export type Int4MultiRange = string & { readonly __pg: "int4multirange" }
export type Int8MultiRange = string & { readonly __pg: "int8multirange" }
export type NumMultiRange = string & { readonly __pg: "nummultirange" }
export type DateMultiRange = string & { readonly __pg: "datemultirange" }
export type TsMultiRange = string & { readonly __pg: "tsmultirange" }
export type TstzMultiRange = string & { readonly __pg: "tstzmultirange" }

// ---- Other branded string types — no accessor written out for any of these yet ; docs/content/typescript/
// index.md ## Typed wire values says which ones a future accessor would even make sense for. ----

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

// ---- Write-side casts ----

/**
 How a write-side cast builds the wire type of a `PgWire` member from the values client code hands over. The
 defaults build the strings JSON carries ; a codec preset that retypes a member in `PgWire` replaces the matching
 entry, so the type and the value stay one decision (presets/ does both in one file). Each entry returns what the
 member's type says, which the compiler cannot check across that boundary.
 */
export const pgWireCasts = {
  /** Requires an offset : Postgres would otherwise silently reinterpret the text in the session's own time zone. */
  timestamptz(value: Date | Temporal.Instant | string): unknown {
    const s =
      typeof value === "string"
        ? value
        : value instanceof Date
          ? value.toISOString()
          : value.toString()
    if (!/[Zz]$|[+-]\d{2}(:\d{2})?$/.test(s)) {
      throw new Error(`asTimestamptz: "${s}" has no time zone offset`)
    }
    return s
  },
  timestamp(value: Temporal.PlainDateTime | Temporal.Instant | Date | string): unknown {
    return typeof value === "string"
      ? value
      : value instanceof Date
        ? value.toISOString()
        : value.toString()
  },
  date(value: Temporal.PlainDate | string): unknown {
    return typeof value === "string" ? value : value.toString()
  },
  bytea(value: Uint8Array | string): unknown {
    return typeof value === "string" ? value : formatBytea(value)
  },
}

/**
 Brands a value as a `Timestamptz`, in the form `PgWire["timestamptz"]` says (an ISO string with an offset by
 default, which the cast checks since Postgres would otherwise read an offset-less string in the session's own
 time zone).
 */
export function asTimestamptz(value: Date | Temporal.Instant | string): Timestamptz {
  return pgWireCasts.timestamptz(value) as Timestamptz
}

/** What a `Timestamp` is read and written as : a wall clock by default, an instant when `PgWire["timestamp"]` says. */
export type TimestampValue = PgWireType<"timestamp", Temporal.PlainDateTime>

/**
 Round-tripping a Temporal.PlainDateTime back to a Timestamp is lossless and unambiguous — PlainDateTime never
 invented a time-zone assumption in the first place. When `PgWire["timestamp"]` is an instant (the server reads it in
 its own time zone), the value is an `Temporal.Instant` instead.
 */
export function asTimestamp(value: TimestampValue | string): Timestamp {
  return pgWireCasts.timestamp(value) as Timestamp
}

export function asPgDate(value: Temporal.PlainDate | string): PgDate {
  return pgWireCasts.date(value) as PgDate
}

export function asMoney(value: string): Money {
  return value as Money
}

/** Brands a plain value as an `Int8` : the value is what `PgWire["int8"]` says it is (a `number` by default). */
export function asInt8(value: PgWireType<"int8", number>): Int8 {
  return value as Int8
}

/** Brands a plain value as a `PgNumeric` : the value is what `PgWire["numeric"]` says it is (a `number` by default). */
export function asNumeric(value: PgWireType<"numeric", number>): PgNumeric {
  return value as PgNumeric
}

export function asInterval(value: Temporal.Duration | string): Interval {
  return (typeof value === "string" ? value : value.toString()) as Interval
}

// ---- Comparators ----

export function compareBigInt(a: bigint, b: bigint): number {
  return a < b ? -1 : a > b ? 1 : 0
}

/**
 Compares two decimals given as text or as numbers (anything whose String() is a plain decimal, which includes
 a lossless-json LosslessNumber) : exact digit-string comparison, no Number() conversion, so it holds for any
 scale. NaN sorts last, Postgres's own convention for its numeric NaN value.
 */
export function compareNumeric(
  left: PgNumeric | string | number,
  right: PgNumeric | string | number,
): number {
  if (typeof left === "number" && typeof right === "number") {
    return left < right ? -1 : left > right ? 1 : 0
  }
  const a = String(left)
  const b = String(right)
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

// ---- Range<T> : one shared, immutable class ; each range kind is Range<T> for whichever T
// its own accessor parses bounds into (Range<number> for Int4Range, Range<bigint> for Int8Range, Range<string>
// for NumRange — the decimal text, not parsed further, so it stays exact — Range<Temporal.PlainDate|PlainDateTime|Instant>
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

// ---- MultiRange<T> : `{` + comma-joined segments, each parsed the same way a single range's
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

// ---- Accessor helpers : each is a function taking a column
// name and returning a one-entry descriptor map keyed by a derived name, composed into `proto` via ordinary
// object spread. A helper's own get/set type `this` explicitly (ThisType doesn't apply outside the literal
// `proto` is checked against directly). ----

// shapes.ts's ExtractDescriptor/IsWritable read a `proto` entry's `get`/`set` as REQUIRED members, so a
// get-only vs. get+set distinction survives to the type level ; casting to `TypedPropertyDescriptor<T>` instead
// would make every field optional-shaped, so neither check could tell writable apart from read-only at all.
type GetSetDescriptor<T, S = T> = { get(): T; set(value: S): void; enumerable: true }
type GetOnlyDescriptor<T> = { get(): T; enumerable: true }

// The wire value of a date or time type is a string unless a codec made it a Temporal value (or a Date) : an
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

export function int8Accessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_bigint`]: {
      get(this: Record<Col, Int8>): bigint {
        return BigInt(this[column] as number | bigint)
      },
      set(this: Record<Col, Int8>, value: bigint | number | string) {
        // a safe integer is a number, anything larger a bigint : what a bigint-aware JSON implementation parses to
        const n = BigInt(value)
        const safe = n >= BigInt(Number.MIN_SAFE_INTEGER) && n <= BigInt(Number.MAX_SAFE_INTEGER)
        this[column] = (safe ? Number(n) : n) as unknown as Int8
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_bigint`]: GetSetDescriptor<bigint, bigint | number | string> }
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
  } as {
    [K in `${Col}_as_duration`]: GetSetDescriptor<Temporal.Duration, Temporal.Duration | string>
  }
}

// ---- Range accessors ----

export function int4RangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, Int4Range>): Range<number> {
        return Range.parse(this[column], Number, (a, b) => a - b)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<number>> }
}

export function int8RangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, Int8Range>): Range<bigint> {
        return Range.parse(this[column], BigInt, compareBigInt)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<bigint>> }
}

export function numRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, NumRange>): Range<string> {
        return Range.parse(this[column], (s) => s, compareNumeric)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: GetOnlyDescriptor<Range<string>> }
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

// ---- Point accessor : the one geometric type prioritized for a real accessor, since it's the
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
  } as { [K in `${Col}_as_point`]: GetOnlyDescriptor<{ x: number; y: number }> }
}

// ---- Money accessor : confirmed against a live Postgres instance
// (en_US.utf8 lc_monetary) — money always renders exactly 2 fractional digits, a leading '-' before the currency
// symbol for a negative value, and ',' as its grouping separator ("$1,234.50", "-$1,234.50", "$0.00"). Returns
// exact whole cents as a bigint, the same reasoning as int8Accessor : a Number()-based conversion would
// reintroduce float precision loss for a large amount. ----

// Strips every character except digits/'.'/'-' before matching, so any currency symbol or grouping character is
// tolerated ; throws instead of silently misparsing under a locale using different separators (e.g. '.' as the
// grouping character), the same discipline as parseInterval's own IntervalStyle assumption.
function parseMoneyCents(text: string): bigint {
  const negative = text.includes("-")
  const digits = text.replace(/[^0-9.]/g, "")
  const m = /^(\d+)\.(\d{2})$/.exec(digits)
  if (!m) {
    throw new Error(`moneyAccessor: "${text}" doesn't match the expected money format`)
  }
  const cents = BigInt(m[1]) * 100n + BigInt(m[2])
  return negative ? -cents : cents
}

// A plain, unformatted decimal string (no currency symbol, no grouping) — confirmed Postgres's own money input
// parser accepts this regardless of lc_monetary, unlike its output formatting.
function formatMoneyCents(cents: bigint | number): string {
  const value = BigInt(cents)
  const negative = value < 0n
  const abs = negative ? -value : value
  const whole = abs / 100n
  const frac = (abs % 100n).toString().padStart(2, "0")
  return `${negative ? "-" : ""}${whole}.${frac}`
}

export function moneyAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_cents`]: {
      get(this: Record<Col, Money>): bigint {
        return parseMoneyCents(this[column])
      },
      set(this: Record<Col, Money>, value: bigint | number) {
        this[column] = formatMoneyCents(value) as Money
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_cents`]: GetSetDescriptor<bigint, bigint | number> }
}

// ---- Bytes accessor : bytea (a bare `string` in hex form, per
// tsgen/types.go's own comment — no brand, unlike every other type here — unless a codec says otherwise) decodes to a Uint8Array, including the
// empty case, confirmed to still render "\\x" (the prefix alone, no digits after it). ----

function parseBytea(text: string): Uint8Array {
  if (!text.startsWith("\\x")) {
    throw new Error(`bytesAccessor: "${text}" doesn't start with the expected "\\x" hex prefix`)
  }
  const hex = text.slice(2)
  const bytes = new Uint8Array(hex.length / 2)
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  }
  return bytes
}

function formatBytea(bytes: Uint8Array): string {
  let hex = "\\x"
  for (const b of bytes) {
    hex += b.toString(16).padStart(2, "0")
  }
  return hex
}

export function bytesAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_bytes`]: {
      get(this: Record<Col, Bytea>): Uint8Array {
        const v: unknown = this[column]
        return typeof v === "string" ? parseBytea(v) : (v as Uint8Array)
      },
      set(this: Record<Col, Bytea>, value: Uint8Array) {
        this[column] = pgWireCasts.bytea(value) as Bytea
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_bytes`]: GetSetDescriptor<Uint8Array, Uint8Array> }
}
