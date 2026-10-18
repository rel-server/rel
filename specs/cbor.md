# CBOR support

rel accepts and produces JSON today via `github.com/bytedance/sonic` end to end : the query DSL parser (`query/expression_parse.go`, `query/node_parse.go`) walks a `sonic/ast.Node` tree, and query results are built entirely in Postgres (`row_to_json`/`json_agg`) and streamed as raw bytes by `server/response.go`'s `streamRows`, never touched by sonic at all. This document specifies CBOR (RFC 8949) as a second wire format alongside JSON, on both the request and response side, without duplicating either the DSL parser or the query compiler.

The IANA media type for CBOR is `application/cbor`. JSON stays the default on every surface this document covers ; CBOR is opt-in per request. CBOR support is always enabled. No configuration flag exists.

> 📜 **ADR**: CBOR support is unconditional (no `http.cbor.enabled` flag) because CBOR is opt-in per request through `Content-Type`/`Accept`, so it cannot change the behavior of existing JSON clients.

## Scope

Three independent surfaces, each with its own section below :

1. **Request decoding** — the query DSL body (`ParseExpression`/`ParseQuery`) accepting CBOR as an alternative to JSON.
2. **Query-engine response encoding** — results of the query compiler (`query/sql.go`), which knows each output field's Postgres type at compile time. Full-fidelity native CBOR.
3. **Declared-route response encoding** — a route function's own returned `jsonb` value (`docs/content/http/requests-responses.md`), which carries no compile-time type information. Token-level transcoding.

## 1. Request decoding

### `Node` interface

Introduce an interface in package `query` abstracting the operations the parsers in `query/expression_parse.go`, `query/node_parse.go` and `query/wellknown_parse.go` use today (a subset of `sonic/ast.Node`'s surface, restricted to the `Strict*` accessors per `AGENTS.md`'s existing mandate) :

```go
type NodeKind int

const (
	KindInvalid NodeKind = iota
	KindNull
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

type Node interface {
	Kind() NodeKind
	StrictString() (string, error)
	StrictBool() (bool, error)
	StrictInt64() (int64, error)
	StrictFloat64() (float64, error)
	// ArrayItems returns every element of an array node, converted once.
	ArrayItems() ([]Node, error)
	ObjectGet(key string) (Node, bool)
	// ObjectEach calls fn for each key of an object node, in no particular order, and stops at the first error.
	ObjectEach(fn func(key string, v Node) error) error
	// Raw returns the node's source text as JSON.
	Raw() (string, error)
}
```

Change every function in those files that currently takes `*ast.Node` or `[]ast.Node` to take `Node` or `[]Node` instead. Call sites that use `Get(...).Exists()` become `ObjectGet`, and call sites that use `MapUseNode()` become `ObjectEach`. `ParseExpression`, `ParseQuery` and `ParseWellKnownFile` keep their `[]byte` signatures and build the sonic adapter internally ; the Content-Type dispatch below picks the adapter at the HTTP boundary.

An array is read through `ArrayItems()` once per array, never per element : the interface has no `ArrayLen`/`ArrayIndex(i)` pair.

> **Why:** an `ArrayLen()`/`ArrayIndex(i)` pair measured 16 to 38 % slower to parse than the current parser, against 5 to 7 % for `ArrayItems()`. `ast.Node.ArrayUseNode()` allocates a new slice on every call, so an index-based loop converts the whole array once per element. Benchmark : `query/parse_bench_test.go`.

`rawWriteQuery.Data` and `rawWellKnown.Params` hold a `Node`, not bytes. `data` is encoded into Postgres values as `specs/typed-write-loading.md ## Value source` describes. `params` are converted at the point they are bound as SQL parameters.

`Raw()` is used only for values Postgres receives as JSON text (`json`/`jsonb` columns, text-mode columns given a map or array, and parameters). On the CBOR adapter it follows the conversion rules of `specs/typed-write-loading.md ## json and jsonb columns`.

> **Why:** CBOR input is not forced through JSON text, which would lose byte strings, tags and non-text keys and cost a conversion of every write payload.

### sonic adapter

A pointer-sized wrapper type over `*sonic/ast.Node` implementing `Node`. Its methods delegate to `ast.Node` as follows :

- `Kind` maps `TypeSafe()` (`V_TRUE` and `V_FALSE` both map to `KindBool` ; `V_ERROR` and anything unknown map to `KindInvalid`).
- `ArrayItems` calls `ArrayUseNode()` once and wraps each element.
- `ObjectGet` calls `Get(key)` and reports `Exists()`.
- `ObjectEach` calls `MapUseNode()` and iterates the result.
- `Strict*` and `Raw` delegate directly.

`ast.Node.Len()` MUST NOT be used to size an array : it returns `0` without an error on a lazily parsed array.

> **Why:** the adapter is not free. Measured against the pre-interface parser with `ArrayItems()`/`ObjectEach` : 5 to 7 % more time to parse a query body (up to about 8 µs on a 50-field body), 20 to 25 % more allocations, and about 1 % more on parse + resolve + compile together. The extra allocations are one `[]Node` per array and one closure per object.

### CBOR adapter

Read the request body in one pass into a flat arena of items (`query/node_cbor.go`), and make each `Node` a pointer to one item. An item records its kind and where its data sits in the body ; a string is copied out, and a number's text built, only when a column asks for it. No Go value is decoded for the document. The same pass validates it, so a malformed body is refused before anything else happens.

> 📜 **ADR**: a purpose-built reader instead of a CBOR library's decode into `any`. The query and write payloads are only ever read, never decoded into Go values, so the general decoder's maps, slices and boxed values were pure cost : on a 50,000-row write body (3.7 MB) the library took 38 ms and the arena reader 6.5 ms, with about 40 % fewer allocations than the JSON path. `fxamacker/cbor` stays as a test-only reference (`query/node_cbor_ref_test.go`) : every document, 65,536 half floats, every truncation, and over a million mutated random documents are read by both and must give the same tree or both be refused.

Limits and rules of the reader:

- Nesting of arrays, maps and tags is limited to 64 levels. A count in a header must be backed by the bytes that remain.
- Text strings must be valid UTF-8, chunk by chunk for an indefinite-length string. Indefinite-length arrays, maps and strings are read.
- Duplicate map keys are refused.
- Simple values other than `false`, `true`, `null` and `undefined` (read as null) are refused. Half, single and double floats are read.
- Tag 0 over text that is not an RFC 3339 date and time, and tag 1 over a non-finite or out-of-range number, are refused.
- A CBOR map key that is not a text string is an error (`errcode`-wrapped, not coerced to a string ; a byte string key is one), as is trailing bytes after the item, and anything that is not well-formed CBOR.
- A CBOR integer, bignum, float or tag 4 decimal fraction is a `KindNumber` node whose `Raw()` is the number's digits : an integer prints as itself, a float so that it never reads as an integer (`3.0`), a tag 4 value as `<mantissa>e<exponent>`. The number encoders of the write path parse that text, so they read a CBOR number exactly as they read a JSON one ; `StrictInt64` and `StrictFloat64` read the CBOR value's own type.
- `Node` has four more kinds, which only CBOR produces : `KindBytes` (a byte string), `KindInstant` (tag 0 or 1), `KindDate` (tag 100) and `KindUUID` (tag 37), read through `StrictBytes`, `Instant` and `Days`. A tag not listed here, or a listed tag with content of the wrong shape, is `KindInvalid` and refused with an error naming the tag.
- `Raw()` converts a map, an array, a string or a null to JSON text (map keys sorted) ; a byte string, a date or time, any other tag, and a non-finite float have no JSON form and are an error.

### Content-Type dispatch

At the HTTP boundary, `POST /rel` selects the adapter from the request's `Content-Type` header, media type parameters and case ignored : `application/cbor` → CBOR adapter, anything else (or absent) → sonic adapter. The server produces a `Node` and calls `query.ParseQueryNode`, which is what `ParseQuery` does after parsing its JSON ; the parsers are unaware of which wire format produced the node. A body that does not decode is refused with `QUERY_MALFORMED_CBOR` (`QUERY_MALFORMED_JSON` for JSON). `GET /rel` is always JSON.

A write's `"data"` stays a `Node`, never JSON text : the write path encodes it into Postgres values directly (`specs/typed-write-loading.md ## Value source`). A well-known query's `"params"` are converted to JSON text by `Raw()`, so a param holding a byte string or a date and time is refused.

## 2. Query-engine response encoding

Applies when the request `Accept`s CBOR (`## HTTP negotiation`). The JSON path (`row_to_json`/`json_agg` in `query/sql.go`, raw bytes passed through by `server/response.go`'s `streamRows`) is unchanged and serves every other request. Implemented in `query/sql.go` (`OutputFormat`, `FormatCBOR`), `query/result_cbor.go` (`ResultEncoder`) and `cborenc/`.

### SQL generation

`CompileSelectFormat(root, FormatCBOR)` (and the `...ForDataNodeFormat` variant a write's read-back uses) builds the same statement shape as `CompileSelect`, with two wrapping expressions changed : a node's row is the record `alias` itself instead of `row_to_json(alias)`, and a to-many embed is `array_agg(sub)` (empty : `'{}'`) instead of `json_agg`. The one result column is a record per row.

A column whose type has no native CBOR form (`nativeCBORType` : the scalars of the type mapping below, enums, domains, and arrays and composites made only of those) is selected as `to_jsonb(col)` instead, and carried as JSON inside the record, then transcoded by `cborenc.AppendJSON`. The range, geometric, network, interval, money and other such types are in this group, so for these a CBOR response says what the JSON one does.

> 📜 **ADR**: records and arrays instead of a CBOR-producing SQL function. Postgres has no `row_to_cbor`, but its binary format for a record is self-describing per field (each field carries its type OID) and so is an array's per element, so nothing needs a JSON intermediate.

### Binary extraction

The statement is run with binary result format (`pgx.QueryResultFormats{pgx.BinaryFormatCode}` as the first argument), and rows are read with `RawValues()` and `FieldDescriptions()`, not `Scan`. A write's read-back, rows kept aside for a rolled-back write, and the fast path's own `RETURNING` rows are all binary records, read by the same encoder (`query.RecordOID`).

### Decoding a field

`ResultEncoder.AppendValue(dst, oid, data)` walks the binary payload :

- Record : field count, then per field `(type OID, length, bytes)`. Field names come from the query's shape (`planFor`, which follows `selectFieldsFor` and the embeds), by position. A record of a named composite type takes its names from introspection (`db.TypeMapByOid`) ; an anonymous one without a plan is `f1`, `f2`...
- Array : dimensions, element OID, then per element `(length, bytes)`. Multi-dimensional arrays become nested arrays.
- A length of `-1` is `null`.
- A truncated or inconsistent payload is an error, never a panic (tested with every prefix).
- Anything else is a leaf, converted by the table below. A type that is unknown, or has no CBOR form, is an error that says so.

No decoding library is involved : the leaves are read straight from the wire bytes and appended with `cborenc`.

### Type mapping

| Postgres type | CBOR |
|---|---|
| `int2`, `int4`, `int8`, `oid` | integer |
| `float4` | single-precision float (exactly the stored value) |
| `float8` | double-precision float |
| `numeric` | tag 4 `[exponent, mantissa]`, keeping the display scale (`1.50` → `[-2, 150]`) ; mantissa a bignum (tag 2/3) beyond 64 bits. `NaN`, `Infinity`, `-Infinity` are floats, which a decimal fraction cannot hold. |
| `timestamptz` | tag 1 : integer seconds, or a double of seconds when it has a fraction. `infinity`/`-infinity` are the text strings. |
| `timestamp` | tag 1, as a `timestamptz` : the instant its wall clock stands for in the connection's `TimeZone`, the one `ts::timestamptz` gives. A reading skipped by a clock change gets the offset in force before it, a reading that occurs twice the offset in force after it. `infinity`/`-infinity` are the text strings. A session time zone rel cannot load refuses the request before the first row. |
| `date` | tag 100 (days since 1970-01-01) ; `infinity`/`-infinity` as text. |
| `uuid` | tag 37 over the 16 bytes |
| `bytea` | byte string, untagged |
| `bool` | true/false |
| `text`, `varchar`, `bpchar`, `name`, `"char"`, enums | text string |
| `json`, `jsonb` | the JSON value as CBOR (maps, arrays, text, tag 4 numbers as in `## 3`) |
| arrays | arrays, `null` elements kept |
| composites, records | maps |
| domains | their base type |
| `void` | `null` |

> 📜 **ADR**: `timestamp` is an instant in the session zone, the value `CURRENT_TIMESTAMP` gives when stored in such a column, so CBOR has one date-time form and no ISO text to parse. The client cannot know which zone the server used : a column that holds wall-clock readings of another zone reads as the wrong instant. Both types keep microseconds as a double of seconds, which a JavaScript `Date` (milliseconds) could not. A `tsrange`, and a `jsonb` holding timestamps, keep their JSON text. `date` uses tag 100 for compactness ; the TypeScript decoder converts it to the representation its types expect.

> 📜 **ADR**: `infinity` values are text because tag 1 and tag 100 cannot hold them, and `null` would lose the difference from a missing value. They are the strings Postgres prints.

Differences from what the earlier draft of this section said : nested arrays and maps have **definite** lengths (each is known when it is written, since the binary payload states its count), and `float4` is a single, not widened.

The JSON-vs-CBOR equivalence is tested by differential tests (`query/result_cbor_test.go`) : for scalars, arrays, composites, JSON, numeric edge cases, the date and time extremes, and compiled queries (nested joins, aggregates, scalar roots), the CBOR is compared with Postgres' own `to_jsonb` of the same value.

### Streaming and framing

The top-level rows of a response are an **indefinite-length array** (`0x9f` ... `0xff`), written as rows arrive, exactly as the JSON path streams `[`...`]` ; an empty result is the definite empty array `0x80`. A failure before the first row is still a clean JSON error response ; after it, the body is truncated (no break byte), the same as truncated JSON. Memory per row is the row's, as in the JSON path.

A request with several items answers a definite array of one answer per item. A response with `count`, `stats`, `sql`, `query_plan` or `returns` is a definite map, `result` last. `count`, `offset` and `limit` are integers. `stats`, `sql` and `query_plan` are small and are converted from their JSON (`query_plan` is Postgres' `EXPLAIN (FORMAT JSON)` output, so its numbers are tag 4).

A scalar function root answers its value itself, in the type mapping above (`null` when the function returned no row).

## 3. Declared-route response encoding

Implemented in `route/encode.go` (`negotiateBody`) and `route/handler.go`.

### Functions returning `json`/`jsonb`, or content declared `application/json`

The body is converted as a whole, in memory, with `cborenc.AppendJSON` : one pass over the text that reads the exact text of each number (never through `float64`) and writes CBOR directly, with no intermediate tree.

- An integer token that fits `int64`/`uint64` → CBOR integer ; one that does not → bignum (tag 2/3).
- A token with `.`, `e` or `E` → tag 4 `[exponent, mantissa]` from its digits, so `1.50` → `[-2, 150]` and the scale survives.
- A string is a text string. No date or UUID tagging : there is no type information to justify it.
- Nested arrays and maps are definite length. An exponent beyond `int64` is refused.

The conversion only runs when the effective `Content-Type` is `application/json`, so `text`, `bytea` and mimetype-domain returns are never touched, and a template's output stays what the template says. A body that cannot be converted is sent as JSON, with a warning in the log. `Vary: Accept` is set on every response whose media type depends on it.

### Functions returning any type

A route function may return any type that has a value, not only `text`, `json`, `jsonb`, `bytea` and mimetype domains (`anyValueType` ; `void`, `record`, polymorphic and other pseudo-types remain unsupported, and `setof` returns do not apply). Such a value is sent as `application/json` or, when CBOR is accepted, `application/cbor` :

- JSON : `coalesce(to_jsonb(fn(...)), 'null')`.
- CBOR, for a type with a native form (`nativeCBORType`) : the call is made with binary result format and the value goes through `ResultEncoder`, so a `timestamptz` is a tag 1, a `numeric` a tag 4, a `bytea` a byte string, a composite a map with its columns' types. No JSON is involved.
- CBOR, any other type (`interval`, `inet`...), or a route with a `template` : the JSON form, converted as above.

The same applies to the second `OUT` column of a full-control function, **except** that its content always takes the JSON-then-convert path, because the envelope column and the content are read from one `select` and the envelope is JSON text.

> 📜 **ADR**: a function's content keeps one shape on the wire (a `numeric` is tag 4 whether it came from a query or from a function returning `numeric`), which is why returning the typed value is better than building `jsonb` by hand, where numbers lose their column types.

## HTTP negotiation

- Request body format : `Content-Type` header, `application/json` (default) or `application/cbor`.
- Response format : `Accept` header (`cborenc.Accepts`). CBOR is answered when `application/cbor` is acceptable and ranks above `application/json` by quality, then by specificity (an exact type beats a range) ; a range alone (`*/*`, none, or `application/*`) means JSON. On a tie of quality and specificity JSON wins, except that a named `application/cbor` beats `*/*`. `q=0` refuses a type.
- The two are independent — a CBOR request body does not imply a CBOR response, and vice versa.
- `Vary: Accept` is sent on `/rel` responses, and on route responses whose type depends on it.
- **Errors are always JSON**, whichever format was accepted, since a client that asked for CBOR can read the error status and code from either, and a stream that fails midway has no clean place for one.
- `GET /rel` also negotiates its response.

## TypeScript client

The client reads and writes through `Querier.prototype.codec` (`typescript/querier.ts`), an object with `encode`, `decode`, and optionally `contentType` and `binary`. A codec with a `contentType` has it sent as both `Content-Type` and `Accept` ; without one, neither header is sent. A failed request is read as text and thrown as `RelRequestError`, never decoded.

`typescript/presets/<name>/` holds one file per codec, copied by hand by the consumer : the codec, and the `PgWire` merge (`typescript/pg_values.ts`) that types the values it yields, in one file. The CBOR preset uses `cbor-x`, decodes tag 1 to `Temporal.Instant`, tag 100 to `Temporal.PlainDate` and tag 37 to a hyphenated string, and sends byte strings untagged. Each preset has its own `tsc` program and its own `bun test` process, run by `test/tsclient`.

> 📜 **ADR**: Presets are not served with `database.ts` : they import third-party packages, which the served file never does.
