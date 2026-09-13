# Typescript Wire Types

Several Postgres types the generated TypeScript client exposes today (`tsgen/types.go`'s `baseScalarTypes`) claim a JS type the wire value doesn't actually have. This spec corrects those mappings, and adds infrastructure (`proto`, `docs/content/typescript/index.md ## Attaching behavior to rows`) for deriving a richer client-side value from the raw wire string without losing type safety or dumping runtime schema data into the generated file.

## Corrected type mapping

`date`, `time`, `timetz`, `timestamp`, and `timestamptz` map to their own distinct branded string type (below), not `Date`.

> **Why:** every one of these arrives over JSON as a plain string ; typing them `Date` type-checks calling `.getFullYear()` on a value that has no such method at runtime. `time`/`timetz` additionally have no date component at all. `timestamp` (no time zone) has no well-defined instant — `new Date()` on it silently assumes the reading client's own local time zone, which is what caused this spec to exist in the first place. `date` doesn't share this specific ambiguity (confirmed : `new Date("2024-01-01")` parses as UTC midnight deterministically, per the ECMA-262 date-time string format's own rule for a date-only string, regardless of the reading machine's time zone) — it's typed as a branded string here anyway, for the same `.getFullYear()`-on-a-string reason every other type in this section is, not because reading it is unsafe. See `## Read-side conversions`.

`money` maps to its own branded string type, not `number`.

> **Why:** confirmed against a live Postgres instance — `to_json`/`jsonb_build_object` render `money` as a locale-formatted, quoted string (`"$10.50"`), not a bare numeral. The current `number` mapping is a stronger version of the same lie the date types tell.

`interval` maps to its own branded string type, not `unknown`.

> **Why:** `interval` isn't in `baseScalarTypes` (`tsgen/types.go`) at all today — it falls to `unknown`, same gap range types had before `## Range types`. Unlike a range or a timestamp, an interval isn't a point in time at all — it's a span, so it doesn't carry the instant-vs-naive ambiguity this spec is otherwise about ; it's included here because it's the same "wire is a string, `tsgen` doesn't say so today" gap, and it has a natural `Temporal` counterpart. See `## Interval`.

`int8` and `numeric` map to their own branded string types, not `number` — the query compiler casts both to text unconditionally (`col::text`) when building `row_to_json`/`jsonb_build_object`, the same fix `money`/the temporal types already needed.

> **Why:** confirmed — Postgres emits both as bare, unquoted JSON numerals, so `JSON.parse` rounds each to the nearest float64 before any client code runs (`9007199254740993` silently becomes `9007199254740992`, a different value, for `int8` ; the same loss hits `numeric` for anything beyond ~15–17 significant digits). Unlike the earlier "out of scope" conclusion for `numeric` in an earlier draft of this spec, this is fixable — a query-compiler change, not a JSON-parser rewrite — once the fix is an unconditional cast rather than a per-value conditional one (see the next `Why`).

> **Why unconditional, not a per-value conditional cast (e.g. `case when col = col::float8::numeric then ... else col::text end`, producing `number | Branded` only for values that don't round-trip) :** considered and rejected. The conditional is strictly worse on every axis that matters : it costs more per row (a cast-and-compare instead of one cast), it's the only value-dependent wire shape anywhere in this spec (every other branded type is unconditionally one shape), and it introduces a real correctness footgun — the same logical column serializes as a `number` in one row and a `string` in another (or the same row over time, as a sequence grows past the safe-integer threshold), so a naive `a.id === b.id` silently breaks the day one side crosses it. The convenience it would buy (small values arrive already unwrapped) is already one accessor call away, consistent with how every other rich value in this spec works.

> **Why a client-side bigint-aware JSON parser (replacing `res.json()` with a hand-rolled tokenizer, à la `json-bigint`/`lossless-json`) was considered and rejected, in favor of the server-side cast :** genuinely avoids touching the query compiler at all, and sidesteps any interaction with `specs/cbor.md` entirely, since it's purely how the client consumes a response. Rejected on two grounds : it fully solves `int8` but only partially solves `numeric` (no fractional part fits `BigInt`, so it would still need its own value-based "preserve this decimal's exact text" rule, decided per-value at parse time rather than per-column at query-compile time) ; and it trades native, JIT-optimized `JSON.parse` for a hand-rolled parser paying a real performance cost on *every* response, not just ones containing large values — unlike the cast plan, which keeps `res.json()` native and only costs anything where an accessor is actually called. Performance and out-of-the-box compatibility (not requiring a custom parser, the same reasoning that makes assuming a native/polyfilled `Temporal` acceptable) outweigh avoiding the query-compiler change.

The `::text` cast has to be added inside `compileSelectField` (`query/sql.go`), which `specs/cbor.md` describes as shared, unaffected by wire format — so it needs a wire-format-aware condition, not a change isolated to a JSON-only code path.

> **Why this corrects an earlier claim in this same discussion :** confirmed by reading `compileSelectField` directly — `case f.column != nil` has no type-driven logic today, and it's the one place a column reference becomes SQL text, used identically regardless of wire format. Adding the cast there unconditionally would incorrectly apply it to a CBOR-requesting query too (which wants the raw value via `pgtype.Map`'s native binary decode, not a pre-cast string). The mitigating fact : `cbor.md`'s own wrapper selection (`row_to_json` vs `row(...)`) already needs a "which wire format" flag threaded through this same compiler, so this conditional rides on plumbing that has to exist anyway rather than being a wholly separate mechanism — smaller in marginal scope than a first read suggested, but a real branch in shared code, not something living safely off to the side.

`int8Accessor` converts `Int8` to `bigint` via `BigInt(value)` — exact, and correct for equality, ordering, and arithmetic with no caveats, since `BigInt()` accepts a string directly and a bigint has none of `Date`/`numeric`'s representation ambiguity.

Comparing two `Int8` values directly as strings (`a.id === b.id`, no accessor) is also correct, for equality specifically.

> **Why, given `## Write-side casts`' `asMoney` `Why` already warns against relying on incidental string canonicalization elsewhere in this spec :** `int8`'s case is different in practice, not just in principle — Postgres's `int8` output is always canonical (no leading zeros, exactly one string per value, confirmed no exception), and `int8`/`bigint` columns are overwhelmingly used for identity (primary/foreign keys), where equality is the only operation that matters, not arithmetic or ordering. `int8Accessor`/`BigInt()` remains necessary for ordering or arithmetic, but requiring it just to compare two IDs would be ceremony without a matching safety benefit.

`numeric` gets no accessor at all, the same as `tsvector`/`xml`/`pg_lsn`.

> **Why:** unlike `int8`, there's no native JS type that represents arbitrary-scale decimals exactly — `BigInt` is integer-only. A `Number()`-based convenience accessor would just reintroduce the precision loss this fix exists to prevent, and raw string equality isn't reliable for `numeric` the way it is for `int8` (`"1.50"` and `"1.5"` can be the same value, different text, depending on scale). Left to the caller's own choice of decimal library, the same philosophy as `Date` vs. `Temporal` — this type is "screwed either way" in the sense that no representation (bare number, cast-to-text, custom bigint-aware parser) gives it a clean, native, zero-dependency rich value the way `int8`/`BigInt` has ; `::text` at least fixes the correctness bug even without offering one.

`inet`, `cidr`, `macaddr`, `macaddr8`, `bit`, `varbit`, `point`, `line`, `lseg`, `box`, `path`, `polygon`, `circle`, `tsvector`, `tsquery`, `xml`, `pg_lsn`, and the six multirange types map to their own branded string types too, not `unknown`.

> **Why:** none of these are in `baseScalarTypes` (`tsgen/types.go`) today — same "falls to `unknown`" gap `interval`/ranges had. See `## Other types` and `## Multiranges` for which of them also get a richer read-side conversion, versus staying a branded string with no accessor.

## Branded string types

Each corrected type above gets one distinct branded type, defined once in the generated preamble and referenced by every column of that Postgres type : `Timestamptz`, `Timestamp`, `PgDate`, `PgTime`, `PgTimetz`, `Money`, `Interval`, `Int8`, `PgNumeric`, `Inet`, `Cidr`, `MacAddr`, `MacAddr8`, `PgBit`, `PgVarbit`, `PgPoint`, `PgLine`, `PgLseg`, `PgBox`, `PgPath`, `PgPolygon`, `PgCircle`, `TsVector`, `TsQuery`, `Xml`, `PgLsn` — the six range types (`## Range types`) and six multirange types (`## Multiranges`) get theirs named in their own sections instead of repeated here.

```typescript
type Timestamptz = string & { readonly __pg: "timestamptz" }
```

A branded type's only purpose is to make one Postgres type's string distinguishable from another's at compile time ; it has no runtime representation; the value is a plain string.

Passing a `Timestamp`/`PgDate`/`PgTime`/`PgTimetz`-branded string anywhere a `Timestamptz` is expected is a compile error.

> **Why:** this is what makes "only `timestamptz` is safe to treat as an instant" a rule the compiler enforces, not just documents.

## Range types

`int4range`, `int8range`, `numrange`, `daterange`, `tsrange`, and `tstzrange` are matched by name directly in `tsgen`'s existing `baseScalarTypes` map (`tsgen/types.go`) and map to their own branded string types (`Int4Range`, `Int8Range`, `NumRange`, `DateRange`, `TsRange`, `TstzRange`) — the same mechanism already used for every other built-in scalar, no new Go-side introspection needed.

> **Why:** a range type is not an array/domain/enum/composite, so `tsTypeExpr` (`tsgen/types.go`) already falls through to the flat `baseScalarTypes` name lookup for it today (confirmed by reading `tsTypeExpr`'s own branch order) — it just isn't in the map yet. `pg.Type` genuinely has no structural range introspection (no `typtype = 'r'` detection, no subtype lookup), but none is needed here : each of these six is a fixed, well-known built-in whose name is matched directly, the same way `"money"`/`"date"` already are, not derived from any subtype. A user-defined custom range type (`CREATE TYPE ... AS RANGE`) still falls to `unknown`, same as any other type outside `baseScalarTypes` today — an accepted, pre-existing limit, not a new gap.

`tsrange`/`tstzrange` bound values use a different textual format than a standalone `timestamp`/`timestamptz` column : a space separator instead of `T`, and (`tstzrange` specifically) a bare `±HH` offset instead of `timestamptz`'s own `±HH:MM`.

> **Why:** confirmed against live Postgres instances, versions 13 through 17 (`tstzrange('2024-01-01 10:00:00+00','...')` renders identically as `"[\"2024-01-01 10:00:00+00\",...)"` on every one tested). An accessor over a range's bounds needs its own parsing pattern ; it cannot reuse a standalone `Timestamptz` accessor's pattern verbatim — see `## Range values` for which accessor each range kind actually gets.

## Write-side casts

Each branded type with a meaningful validity check gets one exported cast function, named `as<Type>` (`asTimestamptz`, `asTimestamp`, `asPgDate`, ...), taking `Date | Temporal.Instant | string` (the temporal-typed ones additionally accept whichever `Temporal` class `## Read-side conversions` produces for them) and returning the branded type.

A `Date`/`Temporal` argument is always valid (it's already an unambiguous instant, or — for `Temporal.PlainDateTime`/`Temporal.PlainDate` — already exactly as unambiguous as the branded string type itself claims to be, no more) ; a `string` argument is validated according to that type's own rule (e.g. `asTimestamptz` requires a trailing `Z` or `±HH:MM`/`±HH` offset) and throws if it fails.

```typescript
export function asTimestamptz(value: Date | Temporal.Instant | string): Timestamptz {
  const s = typeof value === "string" ? value : value.toString()
  if (!/[Zz]$|[+-]\d{2}(:\d{2})?$/.test(s)) {
    throw new Error(`asTimestamptz: "${s}" has no time zone offset`)
  }
  return s as Timestamptz
}
```

> **Why:** this is a write-side, not read-side, helper — deliberately. Converting a wire string to a plain, unbranded `Date`/`Temporal` value is still the caller's own choice of API and needs no helper (`new Date(value)`, `Temporal.Instant.from(value)`, ...). Writing is where the same naive-timestamp ambiguity resurfaces (Postgres would otherwise silently reinterpret an offset-less string using the session's own time zone), so the cast is what catches the mistake, at the exact call site, before it reaches the server. `## Read-side conversions`, below, does add read-side helpers, but for a different reason than convenience — producing the right, already-distinct `Temporal` class for each source, not merely wrapping `new Date()`.

`asMoney`/range casts exist only as a brand-only cast (`value as Money`) with no meaningful runtime check, since nothing about their string shape is unsafe to omit the way a missing time zone offset is.

`asInt8` accepts `bigint | number | string`, converting via `String(value)` — `bigint`/`number` are always valid ; a `string` is passed through as-is, with no client-side re-validation, since Postgres's own `int8` input parser already rejects a malformed one on write (confirmed : `jsonb_populate_record` errors loudly — `"invalid input syntax for type bigint"` — on a genuinely invalid string, it isn't silently lenient). `asNumeric` is the same shape, brand-only, no numeric validation attempted client-side either.

## Read-side conversions

Accessors return the matching `Temporal` class for their source's Postgres type, not a `Date`, and not a type this spec invents :

| Postgres type (via its branded string) | `Temporal` class |
|---|---|
| `Timestamptz` | `Temporal.Instant` |
| `Timestamp` | `Temporal.PlainDateTime` |
| `PgDate` | `Temporal.PlainDate` |
| `PgTime` | `Temporal.PlainTime` |
| `PgTimetz` | none — see below |

> **Why:** this replaces an earlier draft of this section, which defined its own `Date`-based branded subtypes (`Instant`/`AssumedInstant`, `TzInstant`/`NoTzInstant`) to carry the same distinction. Discarded because : (1) every `Date`, once constructed, is a complete, fully-resolved instant — there is no structural difference between a "safe" and an "assumed" one once you're holding the value, so branding one over the other made a claim about the value's shape that wasn't true, only about its provenance ; (2) `Date`'s own mutator methods (`setHours` and similar) mutate in place and return a primitive, not a new branded value, so a compile-time-only brand gives no protection against mutating a value's trustworthiness away ; (3) `Temporal`'s classes are immutable (every "modification" returns a new value, never mutates `this`) and are genuinely distinct classes, not one class with a phantom tag — a `Temporal.PlainDateTime` cannot satisfy a `Temporal.Instant`-typed parameter at all, cast or no cast, which is real structural safety, not a convention. `Temporal.PlainDateTime` in particular needs no assumption baked in by us at all (unlike forcing a naive value through `Date`, which demands picking an interpretation just to construct one) — it's a complete, legitimate value on its own terms, deferring "which time zone" entirely and correctly to whoever eventually calls `.toZonedDateTime(explicitTimeZone)`, which is never this library's call to make.

Which accessor exists already gates which `Temporal` class you get — `timestampTzAccessor` only type-checks against a `Timestamptz`-branded column, `timestampAccessor` only against `Timestamp`, and so on ; no separate return-type branding is needed on top, since the accessor itself is the gate.

`PgTimetz` gets no accessor.

> **Why:** `Temporal` deliberately has no "time of day with a UTC offset, no date" class — considered too rare/ambiguous a combination to standardize. `PgTimetz` stays a branded string with no richer read-side conversion until/unless that changes.

`timestampTzAccessor`/`dateAccessor` (both unambiguous) return `Temporal.Instant`/`Temporal.PlainDate` respectively ; `timestampAccessor` returns `Temporal.PlainDateTime` (naive, zone deferred to the caller, not assumed by us) ; range accessors mirror the same table one level down (`## Range values`), both following the same `_as_date`/`_as_range` accessor pattern as `## Accessor helpers`, below.

A second, separate accessor going the other direction — from a `Timestamptz`'s `Temporal.Instant` to a `Temporal.PlainDateTime` with the zone explicitly discarded (`instant.toZonedDateTimeISO(zone).toPlainDateTime()`) — is a real, distinct need ("give me the wall-clock reading, not the resolved-to-my-zone instant") but not specified further here ; left as a follow-up, not blocking the rest of this section.

`database.ts` references `Temporal` as a global, the same way it already references `Date`/`Promise`/`fetch` without importing them.

> **Why:** the `Temporal` proposal reached Stage 4 (finished, shipping is a matter of when, not if) ; until it's native everywhere, the consuming project is responsible for installing and registering a polyfill (`@js-temporal/polyfill`) as a global, the same kind of environmental assumption `database.ts` already makes for `fetch`. `database.ts` itself imports nothing, unchanged.

## Interval

`intervalAccessor` returns `Temporal.Duration`, the natural counterpart — both represent a span, not a point in time.

The wire format (confirmed against a live Postgres instance, default `IntervalStyle` = `postgres`) :

```
1 day 02:03:04            -- days + time
1 year 2 mons 3 days      -- years/months normalized (13 months -> "1 year 1 mon"), no time part printed if zero... except :
00:00:00                  -- ...an all-zero interval still prints a bare time part
-1 days -02:00:00         -- each of the year/month/day part and the time part carries its OWN sign
1 day -02:00:00           -- confirmed : signs are independent, not one leading sign for the whole value
00:00:01.5                -- fractional seconds, same variable-width rule as timestamp
```

> **Why:** every line above was checked directly, not assumed from documentation — in particular that a day-part and a time-part can carry opposite signs independently (`"1 day -02:00:00"`), which a single-leading-sign parser would get wrong, and that `weeks` never appears in output (a `2 weeks` interval normalizes to `"14 days"`) despite `Temporal.Duration` itself supporting a `weeks` field — the parser never needs to handle a `weeks` token because Postgres never emits one.

`intervalAccessor`'s parser assumes `IntervalStyle` = `postgres` (Postgres's own default) and throws on a string it doesn't recognize, rather than silently misinterpreting a value produced under a different style (`postgres_verbose`/`sql_standard`/`iso_8601` are all differently-shaped text).

> **Why:** same discipline as `asTimestamptz` failing loudly on a missing offset — a session running under a non-default `IntervalStyle` is a real possibility this library can't detect on its own, and a wrong parse here is worse than a thrown error.

`asInterval` (write-side cast) accepts `Temporal.Duration | string` and, for a `Temporal.Duration`, calls its own `.toString()` (ISO 8601 duration syntax, e.g. `"P1DT2H3M4S"`) rather than trying to reproduce Postgres's own quirky output format.

> **Why:** confirmed — Postgres's interval *input* parser accepts ISO 8601 duration syntax regardless of the session's output-side `IntervalStyle` setting (`interval 'P1DT2H3M4S'` parses correctly under the default `postgres` style). Writing is therefore simpler than reading : no need to match Postgres's own text shape at all, just produce a format Postgres's input parser already accepts unconditionally.

## Other types

Confirmed wire formats (each checked directly against a live Postgres instance, not assumed) for every remaining built-in type that fell to `unknown` before this spec, and whether each gets an accessor on top of its branded string :

| Postgres type | Wire format (examples) | Accessor ? |
|---|---|---|
| `inet` | `"192.168.1.1"` (host address — no `/32` suffix), `"10.0.0.0/8"` (non-host — prefix kept) | not specified here ; low value, `.split("/")` covers most needs |
| `cidr` | `"10.0.0.0/8"` (always has a prefix) | same as `inet` |
| `macaddr` | `"08:00:2b:01:02:03"` | none needed |
| `macaddr8` | `"08:00:2b:01:02:03:04:05"` | none needed |
| `bit(n)` | `"10110"` (fixed width) | `_as_number`/`_as_bigint` (`parseInt(s, 2)`) plausible, not specified |
| `varbit` | `"1011010110"` (variable width) | same as `bit` |
| `point` | `"(1.5,2.5)"` | yes — `pointAccessor` returns `{x: number, y: number}`, trivial parse |
| `line` | `"{1,2,3}"` (Ax+By+C=0 coefficients) | not specified — low usage |
| `lseg` | `"[(0,0),(1,1)]"` | not specified |
| `box` | `"(1,1),(0,0)"` | not specified — see `Why`, below |
| `path` | `"[(0,0),(1,1),(2,0)]"` (open) / `"((0,0),(1,1),(2,0))"` (closed) | not specified |
| `polygon` | `"((0,0),(1,1),(2,0))"` | not specified |
| `circle` | `"<(1,1),5>"` | not specified |
| `tsvector` | `"'brown':3 'fox':4 'jump':5 'quick':2"` (stemmed, position-indexed) | none — see `Why` |
| `tsquery` | `"'quick' & 'fox'"` | none |
| `xml` | the literal XML text, unmodified | none — see `Why` |
| `pg_lsn` | `"16/B374D848"` | not specified — rarely a real column type |

> **Why `box` has no accessor specified yet:** confirmed genuinely surprising — Postgres silently reorders `box`'s two corner points into a canonical (top-right, bottom-left) form regardless of input order (`box(point(0,0), point(1,1))` comes back `"(1,1),(0,0)"`, not `"(0,0),(1,1)"`), and the wire format has no enclosing bracket at all (just two comma-joined tuples), unlike every other geometric type. Worth a small value class if wanted, not written out here.

> **Why `line`/`lseg`/`path`/`polygon`/`circle` have no accessor specified yet:** each needs its own small parser (they don't share one shape the way the range types did), and none has a natural existing JS equivalent — would need small value classes, the same idea as `Range<T>`, just not written out per-shape here. `path` in particular encodes open-vs-closed *only* via which bracket character it starts with (`[` vs `(`) — easy to mis-parse if a parser only looks for the coordinate list and ignores the leading character. Given PostGIS (`specs/postgis.md`) already exists as the real answer for anyone doing GIS work, only `point` (the simplest, most commonly used) is worth prioritizing here ; the rest stay branded strings with no accessor unless actually requested.

> **Why `tsvector`/`tsquery` get no accessor:** these aren't data values in the normal sense — they're a stemmed/position-indexed search representation and a query-operator expression, meant for server-side `@@` matching, not client-side consumption. There's no sensible richer JS object downstream code would do anything useful with.

> **Why `xml` gets no accessor:** parsing XML is a solved problem with existing platform APIs (`DOMParser` in a browser) — same philosophy as leaving `Date` vs. `Temporal` to the caller's own choice, this library shouldn't own a redundant XML parser.

`bytea` (already correctly mapped to `string`, hex form, per `tsgen/types.go`'s existing comment) has no accessor yet either ; `bytesAccessor` returning a `Uint8Array` via hex-decode would follow the same pattern with no ambiguity to caveat, including the empty case (confirmed : an empty `bytea` still renders `"\\x"`, the prefix alone with no digits after it) — not written out here, left with `moneyAccessor` under `## Reusable helpers, remaining work`.

## Multiranges

`int4multirange`, `int8multirange`, `nummultirange`, `datemultirange`, `tsmultirange`, and `tstzmultirange` map to their own branded string types (`Int4MultiRange`, `Int8MultiRange`, `NumMultiRange`, `DateMultiRange`, `TsMultiRange`, `TstzMultiRange`), each pairing with the same-named single range type's bound type from `## Range values` (so `Int8MultiRange`'s accessor produces `MultiRange<bigint>`, `NumMultiRange`'s stays bound-as-`PgNumeric` with no further parsing, and so on).

The wire format is `{` + comma-joined individual range segments, each segment identical in shape to its single-range counterpart (including `tsmultirange`/`tstzmultirange` inheriting the exact same quoting/space-separator/offset-width quirks `## Range types` already documents for `tsrange`/`tstzrange`) + `}` — confirmed, including the distinct empty form `"{}"` (a multirange containing zero segments, not the same as one `empty` range).

```
int4multirange('{[1,2),[5,10)}')       -- "{[1,2),[5,10)}"
int4multirange('{}')                    -- "{}"
```

`MultiRange<T>` reuses `Range<T>`'s own per-segment parser directly — split on the outer `{`/`}` and top-level commas, then `Range.parse` each segment with the same `parseBound`/`compare` the corresponding single-range accessor already uses.

> **Why:** this is a small, direct extension of the already-established range design, not a new problem — every quirk a multirange's bounds have were already found and documented for the equivalent single range.

## Arrays

An array of any branded type above (`Timestamptz[]`, `Inet[]`, `Int4Range[]`, ...) needs no special handling beyond what already exists : the wire format is a plain JSON array of the identical per-element string format, and an accessor for an array column is just that column's existing per-element accessor logic applied via `.map()`.

> **Why:** confirmed directly (`timestamptz[]`, `inet[]`, `int4range[]` all checked) — nothing about array-wrapping changes an element's own format. `tsgen`'s existing `IsArray()` handling (`tsTypeExpr`, `tsgen/types.go`) already wraps any base type in `[]` uniformly ; this spec's branded types flow through that exact same mechanism with no separate array-specific design needed.

## `proto` as a property-descriptor map

`RelationQuery`'s `proto` field is a map of property name to either a plain function (an ordinary method) or a property descriptor (`{get, set, enumerable}` and similar), not a plain object of getters/methods.

```typescript
proto?: { [name: string]: ((...args: any[]) => unknown) | TypedPropertyDescriptor<unknown> }
```

> **Why:** a plain-object `proto` (the prior design) can't compose reusable accessor helpers safely. `Object.assign`/spread on an object whose OWN properties are live accessors evaluates the accessor once and copies the resulting value as dead data — confirmed empirically — so a helper meant to be spread into `proto` has to return descriptors, one level removed from being live accessors itself, and a plain object literal's own `get`/`set` syntax doesn't have that shape. Composing multiple descriptor-maps via ordinary spread is safe, since each entry is a plain data property whose value happens to be a descriptor object, not an accessor itself.

`this` inside a descriptor's `get`/`set`, or inside a plain method, is typed against the query node's own computed row shape the same way the prior plain-object design's `this` was (`ThisType`, `shapes.ts`'s `WithProto`) — confirmed empirically that `ThisType` still resolves correctly for `get`/`set` nested one level inside a named descriptor value, not just for an object's own top-level accessor syntax.

`ExtractDescriptor<D>`/`IsWritable<D>` (`shapes.ts`) read a `proto` entry's effective member type and whether it's assignable :

```typescript
type ExtractDescriptor<D> = D extends (...args: any[]) => any
  ? D
  : D extends { get(): infer T; set(v: any): void }
    ? T
    : D extends { get(): infer T }
      ? T
      : D extends { value: infer V }
        ? V
        : unknown

type IsWritable<D> = D extends (...args: any[]) => any
  ? false
  : D extends { set(v: any): void }
    ? true
    : false

type FromDescriptorMap<P> =
  { readonly [K in keyof P as IsWritable<P[K]> extends true ? never : K]: ExtractDescriptor<P[K]> } &
  { -readonly [K in keyof P as IsWritable<P[K]> extends true ? K : never]: ExtractDescriptor<P[K]> }
```

The writable half of `FromDescriptorMap` MUST use `-readonly`, not bare `readonly`-less mapped-type syntax.

> **Why:** confirmed empirically, and a real bug caught in this exact draft — a homomorphic mapped type over `P` inherits `P`'s own `readonly` modifier per key by default, and every property of a `const`-inferred type parameter is deeply `readonly`. Without the explicit `-readonly`, every entry comes out non-assignable regardless of `IsWritable`'s own result, silently defeating the whole split.

A `proto` entry with only a `get` (no `set`), or a plain method, is `readonly` in the merged shape ; a `get`/`set` pair is assignable.

`MergeProto` (`shapes.ts`) merges `FromDescriptorMap<P>` into both `ShapeFromRelationQuery` (already true) and `WriteShapeFromRelationQuery` (new).

> **Why:** a prior version of this feature deliberately kept `proto` out of `WriteShape`, reasoning `proto` only ever added read-side getters. That reasoning no longer holds now that `proto` can add a `set` too — assigning a virtual, writable property (`row.created_at_as_date = new Date()`) while building a write payload needs that property to type-check, the same way it needs to read-check. A get-only entry still correctly comes out `readonly` (non-assignable) in `WriteShape` via the same `IsWritable` split, so this doesn't let a read-only virtual property leak into a write payload.

## Runtime construction

`query.proto`, being a descriptor map rather than a directly-usable prototype, is turned into the actual prototype object once per query node via `Object.defineProperties({}, ...)`, cached (a `WeakMap` keyed by the query node), and reused across every row that node produces — not reconstructed per row.

A plain-function `proto` entry is wrapped as a data descriptor (`{value: fn, enumerable: true}`) before being handed to `Object.defineProperties` ; an already-descriptor-shaped entry passes through unchanged.

## Accessor helpers

A reusable accessor helper is a function taking a column name and returning a one-entry descriptor map keyed by a derived name, composed into `proto` via ordinary object spread (safe here — see `proto`'s own `Why`, above).

```typescript
function timestampTzAccessor<Col extends string>(column: Col) {
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

function timestampAccessor<Col extends string>(column: Col) {
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
```

> **Why `timestampAccessor` gets a setter now, unlike the discarded `Date`-based draft:** going `Temporal.PlainDateTime` back to a `Timestamp` string is lossless and unambiguous — `PlainDateTime` never invented a time-zone assumption in the first place, so round-tripping it doesn't carry the "which convention" problem a `Date`-based naive value did.

```typescript
const properties = relation("hotel.properties", {
  proto: {
    ...timestampTzAccessor("created_at"),
    describe(): string {
      return `${this.name} (${this.id})`
    },
  },
})

properties[0].created_at            // Timestamptz — the raw column, untouched
properties[0].created_at_as_date    // Temporal.Instant — a new, derived property, not a shadow of created_at
properties[0].created_at_as_date = Temporal.Now.instant()
```

A helper's own `get`/`set` type their `this` explicitly (`Record<Col, Timestamptz>`), not via `ThisType`.

> **Why:** `ThisType` only applies to an object literal written directly in the position `proto`'s own type gives contextual meaning to. A helper is a separate function call ; its result merges into that position, but the helper's own body is checked outside it.

The suffix convention is `_as_<kind>` (`_as_date` for a temporal accessor) ; a money accessor would be `_as_number`, a range accessor `_as_range`.

Accessor helpers and cast functions need no `/* @__PURE__ */` annotation or other tree-shaking hint to be dropped from a bundle when unused.

> **Why:** confirmed with `esbuild` — a plain, unused exported function declaration (no side effects of its own) is already eliminated by ordinary dead-code elimination with zero annotations, in both a direct bundle test and by pattern (an unused factory function was dropped identically). `/* @__PURE__ */` specifically marks a *call expression* whose result might be discarded as safe to remove (e.g. a side-effecting-looking call at module scope) ; it has no bearing on whether an unused top-level function *declaration* survives bundling, which every bundler already handles for a file with no other side effects.

## Reusable helpers, remaining work

`moneyAccessor`, `bytesAccessor` (`## Other types`), and `pointAccessor` (`## Other types`) each follow the exact same pattern as `timestampTzAccessor` ; not written out here since the pattern is now established.

## Range values

`Range<T>`, one shared generic class (defined once, not duplicated per range kind — `Int4Range`/`Int8Range`/`TstzRange` etc. are each just `Range<number>`/`Range<bigint>`/`Range<Temporal.Instant>` once parsed, per the table below), immutable, no public constructor :

```typescript
class Range<T> {
  private constructor(
    readonly lower: T | null,          // null = unbounded
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
  ): Range<T> { /* "[1,10)" / "empty" bracket-notation parse, shared across every range kind */ }

  contains(value: T): boolean { /* respects lowerInclusive/upperInclusive, empty always false */ }
  overlaps(other: Range<T>): boolean { }
  equals(other: Range<T>): boolean { }
  toString(formatBound: (v: T) => string): string { /* round-trips back to Postgres's own range text */ }
}
```

`compare` is supplied once, at parse time (by the accessor, per range kind), not passed to `contains`/`overlaps`/`equals` on every call.

Every range gets an accessor, mirroring `## Read-side conversions`' own table one level down : `int4range` returns `Range<number>` (bounds are `int4`, always safely representable) ; `int8range` returns `Range<bigint>` (bounds converted the same way `int8Accessor` does, via `BigInt`) ; `numrange` returns `Range<PgNumeric>` — the *bounds themselves* are left as branded strings, not parsed further, matching `numeric`'s own no-accessor policy ; `daterange` returns `Range<Temporal.PlainDate>` ; `tstzrange` returns `Range<Temporal.Instant>` (bounds already carry an offset, confirmed : `"2024-01-01 10:00:00+00"`) ; `tsrange` returns `Range<Temporal.PlainDateTime>` (naive bounds — `Temporal.PlainDateTime`, not `Date`, so no assumption is injected here either, same reasoning as `timestampAccessor`).

> **Why `int8range`'s bounds need the same treatment as a bare `int8` column:** the same precision loss applies identically to a range's bounds as to a scalar column — `int8range`'s wire text itself doesn't lose precision (it's already inside a JSON string, the range's own bracket notation), but naively parsing a bound with `Number(bound)` inside the accessor would reintroduce exactly the bug this spec exists to close. `Range.parse`'s `parseBound`/`compare` for `int8range` use `BigInt`/`bigint`-aware comparison instead of `Number`/numeric subtraction.

```typescript
function tstzRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, TstzRange>): Range<Temporal.Instant> {
        return Range.parse(this[column], (s) => Temporal.Instant.from(s), Temporal.Instant.compare)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<Temporal.Instant>> }
}

function tsRangeAccessor<Col extends string>(column: Col) {
  return {
    [`${column}_as_range`]: {
      get(this: Record<Col, TsRange>): Range<Temporal.PlainDateTime> {
        return Range.parse(this[column], (s) => Temporal.PlainDateTime.from(s), Temporal.PlainDateTime.compare)
      },
      enumerable: true,
    },
  } as { [K in `${Col}_as_range`]: TypedPropertyDescriptor<Range<Temporal.PlainDateTime>> }
}
```

`compare` no longer needs a hand-written comparator for any `Temporal`-typed range — every `Temporal` class exposes its own static `.compare` (`Temporal.Instant.compare`, `Temporal.PlainDateTime.compare`, `Temporal.PlainDate.compare`), passed straight through to `Range.parse`.

## `create()`

A `Querier.create()` method, reusing `this.query`'s own `proto` tree recursively (spanning `join`), so a caller can build a new row (for `.write()`) with the same accessors as a read row, without a manual `Object.setPrototypeOf` per level.

Cardinality (whether a join key should be `[]` or a single nested object) becomes recoverable from the shortcut string alone, by widening its direction marker from two characters to three, each telling both direction and cardinality on its own : `>` outgoing (necessarily to-one — this relation owns the FK, unchanged), `<` incoming-and-unique (to-one — a 1:1 relationship modeled via a unique incoming FK, redefined from its current meaning), `*` incoming-and-multiple (to-many — the joined relation owns the FK, not unique).

> **Why:** this redefines `<`'s current meaning (today, incoming defaults to to-many) rather than only adding a marker ; the common incoming-to-many case earns the visually plainest symbol (`*`, reading naturally as "multiple"), and `<`/`>` become a symmetric pair — a relationship is unique in exactly one of two directions, `<` or `>`, with `*` marking the one shape that isn't. Acceptable as a redefinition, not just an addition, since shortcuts are `tsgen`-generated (`schema.example.ts`'s own header comment), not typically hand-authored — a regenerated `database.ts` is internally consistent regardless of which convention it was generated under, and this feature is still evolving pre-stable.

The `;` separator between the direction marker and the column-pair list is dropped ; the (now three-way) marker character itself is the separator, since none of `<`/`>`/`*` can otherwise appear in an unquoted schema/relation/column name.

```
hotel.rooms*id:property_id            -- to-many, incoming (was '<' before this redefinition)
hotel.properties>id:property_id       -- to-one, outgoing (unchanged)
hotel.profile<id:user_id              -- to-one, incoming-but-unique (was Relationships' separate unique:true case)
```

`Relationships`' separate `unique: boolean` field (`schema.example.ts`) is retired ; `JoinCardinality` (`shapes.ts`) derives cardinality by pattern-matching the shortcut's own template literal type instead of a lookup.

> **Why:** carrying both the marker and a redundant `unique` field would be two sources of truth for the same fact.

With the marker carrying full cardinality, `create()` needs no runtime schema data and no unresolved guess : walking `this.query`'s `join` map, each key's own shortcut marker says definitively whether to seed it with `[]` (to-many, `*`) or a single `create()`-built nested value (to-one, `<` or `>`).

## GIS types and other extensions

Out of scope for this spec — see `specs/postgis.md`. `citext` is the one popular extension type already handled today (`baseScalarTypes` already maps it to `string`, needing no correction).
