# CBOR columns, CBOR routes, compressed requests, declared types

Decisions are in `docs/adr/0012` to `0014`. This is what to build.

## CBOR columns

- Done : recognised by type name at introspection (`pg.Type.CBOR`) : a domain named `cbor` over `bytea`, postcbor's base type `cbor`, or a domain over either. Both use the same encoder ; postcbor's binary receive takes raw CBOR.
- Done : write, the item's bytes, sliced from the body when no structure sits inside, spliced otherwise (ADR 0012). Never re-encoded from the node tree. A JSON write whose selection holds a CBOR column is refused in `relexec.Prepare` (`query.CBORWritten`, `CBOR_COLUMN_NEEDS_CBOR`), and a JSON value again when encoded. An array of a CBOR type has one dimension : an element that is an array is a CBOR item.
- Done : reader (`query/node_cbor.go`) : each node keeps its byte range, the document the offsets of its structures, `itBadTag` its content node ; a map with a non-text key (`itOpaqueMap`) and an unassigned simple value (`itSimple`) are read, of no Node kind, for a CBOR column only. `appendCBOR` writes an item spliced.
- Done : CBOR response, the stored bytes copied as they are, structures or not, a bytea domain's once checked to be one well-formed item (`cborenc.WellFormed`), postcbor's as is. Inside records and arrays a domain keeps its own OID ; at the root of a statement Postgres sends a bytea domain as `bytea`, so the result encoder knows it from the query (`rootBytesAreCBOR`) : a scalar function returning one, or a scalar select whose expression holds one.
- Done : JSON response holding a CBOR column's value (default selection included) refused in `relexec.Prepare` (`query.CBORReached`, ADR 0013), only when the database has a CBOR type. A node named as a value is its relation's whole row ; each node is walked once. Casts to a type named `cbor` count ; a domain under another name is only seen through a column.
- Done : tsgen types a CBOR column `unknown`.
- Filters on CBOR columns : later, once postcbor's operators exist.

## Routes and well-known params

- Done : `fn(req jsonb, body cbor)` (`Route.CBORArg`) : like `file bytea`, the whole body spliced (`query.SpliceCBOR` : only a body with a structure tag is read into nodes, any other is checked well-formed by `cborenc.Scan` and passed as is), bound as `$n::bytea::<type>` (postcbor's cast from `bytea` reads the bytes), `req.body` null, `POST` by default. Another media type, JSON included, is a 415 ; unreadable CBOR a 400 ; an empty body NULL. No `cbor[]` argument, no CBOR `stream_upload` (a part comes as `bytea[]` and is cast in the function).
- Done : a CBOR column type return (`Route.ContentCBOR`), single or full-control content : selected `::bytea`, sent as `application/cbor` whatever the `Accept`. A NULL single return is `f6`, a NULL full-control content no body. `stream_upload` calls share the full-control SQL (`fullControlSQL`). A bytea-domain return is not checked well-formed. A value holding one (a composite, an array) is answered in native CBOR whatever the `Accept`.
- Done : a well-known param declared with a CBOR type name : bound from its item's bytes, spliced (`query.CBORParamValue`) ; from JSON, a 400 (`WELL_KNOWN_PARAM_TYPE_MISMATCH`). A cbor cast binds it as bytes (`$n::bytea::<cast>`) ; load refuses a cbor param under another cast, a cbor cast on another param, and a default other than null.

## Compressed requests (ADR 0014)

- `rejectEncodedBody` (`boot/mux.go`) becomes a decompressing reader for `gzip`, `deflate`, `zstd` ; anything else, or more than one coding, stays a 415.
- `http.max_body_size` bounds the decompressed bytes.
- The TypeScript codec gzips bodies above a threshold (default about 1 KB, configurable) through `CompressionStream`. It does not change its use of structures.
- Responses : still the proxy's job.

## Declared types (tsgen)

- Done : a comment prefixed by its object kind is HUML (or JSON after `:` and `{`), as `route::` is (`declcomment.Extract`, shared with routes) : `column::`, `table::`, `type::`. Without a prefix, a comment stays plain doc text.
- Done : fields `comment` (the `/** */` text), `type` (any TS type expression, emitted as written, the whole column's type), `from` (optional : `type` is then a name imported from that module, aliased `Name__2` when it collides with another import or any name the file uses). `table::` has only `comment` ; `type::` takes a type on a domain only.
- Done : applies to `json`, `jsonb` and `cbor` columns and domains over them ; undeclared stays as today (`unknown` for `cbor`). A column's declaration wins over its domain's.
- Done : config `typescript.types.<schema>.<table>.<column>` (nested, or one quoted dotted key) wins over a comment, field by field, with a warning. A key naming no column is a warning.
- Done : a well-known param's `ts` object, on a json, jsonb or cbor param or one of a type rel does not know. Result fields need nothing : a selected column keeps its declared type through `shapes.ts`.
- Done : `database.json` carries `ts_type`, `ts_from` and the parsed `comment`.
- Done : a broken declaration is left out and logged once per snapshot build, and by `--typescript-out` ; generation itself is silent.
- Done : under the `json` codec, a CBOR value is `never` (column, nullable or not, domain, param) : a write fails to compile, a select does not, and the server refuses it.
- Later : route `body` and `returns` declarations, once tsgen types routes (it has no route types today).
- rel never checks a value against its declared type ; the docs point to Postgres `CHECK` constraints (`pg_jsonschema`).
