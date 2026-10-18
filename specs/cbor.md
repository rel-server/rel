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

Decode the request body with `cbor.Unmarshal` (library : `fxamacker/cbor/v2`) into `any`, with maps decoded to `map[string]any`, tags allowed, tags 0 and 1 decoded to `time.Time`, integers beyond 64 bits to `big.Int`, duplicate map keys refused and nesting limited to 64 levels. Wrap the result in a second `Node` implementation, `cborNode` (`query/node_cbor.go`).

> 📜 **ADR**: One decode call instead of a lazy walk. Query DSL bodies are small (a query tree, not a data payload), so sonic's lazy/SIMD advantage does not matter here ; `fxamacker/cbor` has no AST type equivalent to `sonic/ast`, so full decode is the only option anyway.

- A CBOR map key that is not a text string is an error (`errcode`-wrapped, not coerced to a string), as is a repeated key, trailing bytes after the item, and anything that is not well-formed CBOR.
- A CBOR integer, bignum, float or tag 4 decimal fraction is a `KindNumber` node whose `Raw()` is the number's digits : an integer prints as itself, a float so that it never reads as an integer (`3.0`), a tag 4 value as `<mantissa>e<exponent>`. The number encoders of the write path parse that text, so they read a CBOR number exactly as they read a JSON one ; `StrictInt64` and `StrictFloat64` read the CBOR value's own type.
- `Node` has four more kinds, which only CBOR produces : `KindBytes` (a byte string), `KindInstant` (tag 0 or 1), `KindDate` (tag 100) and `KindUUID` (tag 37), read through `StrictBytes`, `Instant` and `Days`. A tag not listed here, or a listed tag with content of the wrong shape, is `KindInvalid` and refused with an error naming the tag.
- `Raw()` converts a map, an array, a string or a null to JSON text (map keys sorted) ; a byte string, a date or time, any other tag, and a non-finite float have no JSON form and are an error.

### Content-Type dispatch

At the HTTP boundary, `POST /rel` selects the adapter from the request's `Content-Type` header, media type parameters and case ignored : `application/cbor` → CBOR adapter, anything else (or absent) → sonic adapter. The server produces a `Node` and calls `query.ParseQueryNode`, which is what `ParseQuery` does after parsing its JSON ; the parsers are unaware of which wire format produced the node. A body that does not decode is refused with `QUERY_MALFORMED_CBOR` (`QUERY_MALFORMED_JSON` for JSON). `GET /rel` is always JSON.

A write's `"data"` stays a `Node`, never JSON text : the write path encodes it into Postgres values directly (`specs/typed-write-loading.md ## Value source`). A well-known query's `"params"` are converted to JSON text by `Raw()`, so a param holding a byte string or a date and time is refused.

## 2. Query-engine response encoding

This tier applies only when the client asks for CBOR (`Accept: application/cbor`). The existing JSON path (`row_to_json`/`json_agg` in `query/sql.go`, raw-byte passthrough in `server/response.go`'s `streamRows`) is unchanged and used for every other request.

### SQL generation

For a CBOR-requesting query, replace `row_to_json(alias)` with `row(alias)` and `coalesce(json_agg(row_to_json(sub)), '[]'::json)` with `array_agg(row(sub))` in `wrapNodeAsValue` and `compileEmbedField` (`query/sql.go`). Everything else about `CompileSelect`'s shape — the LATERAL-sharing plan, `compileShapeAsJsonObject` (`query/sql_expr.go`) for key/shape objects, `countMode` — is unaffected ; only the two wrapping expressions differ, selected by which wire format the request asked for.

> 📜 **ADR**: row/array_agg instead of a CBOR-producing SQL function. Postgres has no `row_to_cbor` builtin. Postgres's own binary wire encoding for a composite value (`ROW(...)`, including the anonymous `record` pseudo-type) is self-describing per field — each field carries its own type OID inline — and array binary encoding is self-describing per element the same way. That removes the need for row_to_json's JSON-text intermediate entirely.

### Binary extraction

Execute the compiled statement requesting binary result format for the composite/array column, using pgx's low-level API (`pgx.Rows.RawValues()` + `FieldDescriptions()`) instead of `rows.Scan(&raw)` — `streamRows`'s current single `Scan` into `[]byte` only works for the JSON path, where the column is already `json`/`jsonb` text.

### Decoding a field

Walk the composite/array binary payload recursively :

- Composite : read field count, then per field `(type OID, length, bytes)` in order.
- Array : read dimensions/element OID/null bitmap, then per element `(length, bytes)`.
- A length of `-1` is SQL `NULL` → CBOR `null`.

Field **names** come from the query's own compiled `Shape` (`query/sql_expr.go`), not from Postgres — `ROW(...)` results are positional. Match binary field position to `Shape`'s field order 1:1.

Decode each leaf's bytes via `pgtype.Map`'s existing per-OID codec (`jackc/pgx/v5/pgtype` already registers a codec for every built-in type — no `LoadType`/`RegisterType` call needed for these), then encode the resulting Go value as CBOR per the type mapping below.

### Type mapping

| Postgres type | Go value (`pgtype.Map`) | CBOR encoding |
|---|---|---|
| `int2`/`int4`/`int8` | `int64` | CBOR integer (major type 0/1) |
| `numeric` | `pgtype.Numeric` | tag 4 (decimal fraction: `[exponent, mantissa]`) ; mantissa as a CBOR bignum (tag 2/3) if it exceeds 64 bits |
| `timestamptz` | `time.Time` | tag 1 (epoch-based date/time), as a CBOR double (fractional seconds since epoch) |
| `timestamp` | `time.Time` (UTC-labeled wall clock) | see the open question below |
| `date` | `time.Time` | tag 100 (RFC 8943 epoch-based date : integer days since 1970-01-01) |
| `uuid` | `[16]byte` | tag 37 (binary UUID) |
| `bytea` | `[]byte` | plain CBOR byte string (major type 2), no tag |
| `bool` | `bool` | CBOR true/false |
| `text`/`varchar`/... | `string` | CBOR text string (major type 3) |
| everything else | `pgtype.Map`'s default Go value | nearest CBOR equivalent by Go kind |

> 📜 **ADR**: `date` uses tag 100 (integer days) instead of tag 1004 (string) because it is compact and needs no parsing. The TypeScript decoder must convert tag 100 to the representation its client types expect (the TypeScript client currently types `date` as a plain string).

> 💬 You ask how a JavaScript client can decode `timestamp` afterwards, and how Postgres stores `timestamptz` and `timestamp`.

> 🤖 Postgres stores both types as the same 8-byte integer : microseconds since 2000-01-01 00:00:00. The difference is the meaning. `timestamptz` is a UTC instant ; the session time zone only applies on input and output. `timestamp` is a wall-clock reading with no zone, and Postgres never converts it. pgx decodes `timestamp` into a `time.Time` whose location is UTC, so the Go value looks like an instant but is not one.
> If `timestamp` is sent as tag 1, a JavaScript `Date` shows the correct wall-clock digits only if the client reads it with `getUTC*` methods. Any code that uses local-time methods, or `toISOString()` and then compares it with real instants, silently shifts the value. Nothing in the encoded bytes tells the decoder that the value is not an instant. Also, a JavaScript `Date` keeps milliseconds only, so the microseconds Postgres stores are lost for both types.
> Recommendation : encode `timestamp` as a plain CBOR text string in ISO 8601 form without offset (`2026-10-18T14:30:00.123456`). This keeps microseconds, matches how the TypeScript client already types `timestamp` (plain string), and cannot be mistaken for an instant. Only `timestamptz` gets tag 1.

> ❓ **Question**: Do you accept encoding `timestamp` as an untagged ISO 8601 text string without offset, and `timestamptz` as tag 1 ? Also decide the encoding of the Postgres values `infinity`/`-infinity` for `timestamp`, `timestamptz` and `date` (pgx returns an infinity modifier instead of a `time.Time`) : tag 1 cannot represent them. Options : CBOR `null`, or the text strings `"infinity"`/`"-infinity"` (as Postgres prints them).

### Streaming and framing

Encode CBOR indefinite-length arrays/maps (major type 4/5 header byte, items, `0xff` break) for the top-level row stream and for every nested array/object produced during decoding, never definite-length. This mirrors the existing hand-rolled `"["`/`","`/`"]"` JSON framing in `streamRows` one level down, and avoids buffering a sub-structure to learn its item count before writing its header.

Per-row memory cost stays O(nesting depth), matching the JSON path's constant-memory guarantee — no full-row Go value is built before encoding starts.

## 3. Declared-route response encoding

A route function's returned `jsonb` (`docs/content/http/requests-responses.md` ## Function prototype) carries no compile-time `Shape` — it is whatever JSON the function computed. CBOR output for this surface is a token-level JSON→CBOR transcode, not the native path above :

- Read the returned JSON with a streaming tokenizer that exposes the exact text of each number token (`encoding/json.Decoder.UseNumber()`, or sonic's equivalent), never through `float64`.
- Encode each token to the matching CBOR token as it is read — no intermediate tree.
- A number token without `.`, `e` or `E` that fits `int64`/`uint64` → CBOR integer.
- A number token without `.`, `e` or `E` that does not fit → CBOR bignum (tag 2/3), parsed from the token's exact digit text.
- A number token with `.`, `e` or `E` → tag 4 (decimal fraction) `[exponent, mantissa]`, computed from the token's exact digit text : mantissa = all digits without the decimal point (signed), exponent = the exponent part minus the count of digits after the decimal point. The mantissa is a CBOR bignum (tag 2/3) if it exceeds 64 bits. No `float64` conversion occurs, so the trailing scale is kept (`1.50` → `[-2, 150]`).
- A string token is always encoded as a plain CBOR text string. No date/UUID tagging — there is no per-field type information available to justify it here.

> 📜 **ADR**: Every `jsonb` number is a Postgres `numeric` internally, so tag 4 for fractional tokens is the type-correct, lossless choice, and it matches the `numeric` row in section 2. The TypeScript decoder decides how to represent tag 4 values (for example as a decimal library type or as a `number`).

## HTTP negotiation

- Request body format : `Content-Type` header, `application/json` (default) or `application/cbor`.
- Response format : `Accept` header, `application/json` (default) or `application/cbor`.
- The two are independent — a CBOR request body does not imply a CBOR response, and vice versa.

> ⌚ **Deferred**: The TypeScript client (`tsgen/`) needs its own CBOR encode/decode to use this from a browser/Node client. This includes the JS-side CBOR library, and the JS representation of tag 1, tag 4 and tag 100 values. It needs its own pass once the Go side lands.
