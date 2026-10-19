/*!
docs/content/typescript/index.md ## Typed wire values : branded string types for every Postgres type whose JSON wire representation
isn't the JS type it might look like, plus the write-side casts and read-side accessors for deriving a richer
client-side value from one, without losing type safety or requiring a runtime dependency. Concatenated first
(tsgen/embed.go), so the generated schema section and every other section can reference these names directly.
*/

// ---- Branded date and time types ----

export type Timestamptz = PgWireType<"timestamptz", string> & { readonly __pg: "timestamptz" }
export type Timestamp = PgWireType<"timestamp", string> & { readonly __pg: "timestamp" }
export type PgDate = PgWireType<"date", string> & { readonly __pg: "date" }
export type PgTime = string & { readonly __pg: "time" }
export type PgTimetz = string & { readonly __pg: "timetz" }
export type Interval = string & { readonly __pg: "interval" }

// ---- Types whose JS value depends on the codec : typed by what the codec in use yields ----

/**
 What the JS values of the Postgres types that the codec in use may hand over as something other than the JSON
 default actually are. It is empty here : the generated file adds the members of the codec options it was built
 with (`?codec=` of docs/content/typescript/index.md ## Codecs) in an `interface PgWire` of its own, at the end of
 the file. The values the write-side casts build must match what a member says : see `pgWireCasts`.
 */
// biome-ignore lint/suspicious/noEmptyInterface: filled in by the generated file's codec block
export interface PgWire {}

/** The JS type a wire member of `PgWire` stands for : its augmented type, or `Default`. */
export type PgWireType<K extends string, Default> = PgWire extends { [P in K]: infer T }
  ? T
  : Default

/** `bytea` : the `\x…` hex text JSON gives, or whatever `PgWire["bytea"]` says (a `Uint8Array` with the `cbor` codec). */
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
 How a write-side cast builds the wire value of a date or time type (or of `bytea`) from what client code hands
 over. The time and wire chunks of the generated file fill it in (`codecs/time-*.ts`, `codecs/cbor-*.ts`), so the
 cast and the codec that writes the value stay one decision. A cast is the place to override how a value is
 turned into what the codec writes.
 */
// biome-ignore lint/suspicious/noExplicitAny: each chunk narrows the argument its own cast takes
export const pgWireCasts: Record<string, (value: any) => unknown> = {
  bytea(value: Uint8Array | string): unknown {
    return typeof value === "string" ? value : formatBytea(value)
  },
}

/** Throws unless a timestamp text ends with a UTC offset : Postgres would read it in the session's own time zone. */
export function requireOffset(text: string): string {
  if (!/[Zz]$|[+-]\d{2}(:\d{2})?$/.test(text)) {
    throw new Error(`asTimestamptz: "${text}" has no time zone offset`)
  }
  return text
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

export function asInterval(value: string): Interval {
  return value as Interval
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
// for the temporal ranges, see codecs/time-temporal.ts). ----

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
export type GetSetDescriptor<T, S = T> = { get(): T; set(value: S): void; enumerable: true }
export type GetOnlyDescriptor<T> = { get(): T; enumerable: true }

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
