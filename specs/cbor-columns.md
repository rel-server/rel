# CBOR columns, CBOR routes, compressed requests, declared types

Decisions are in `docs/adr/0012` to `0014`. This is what to build.

## CBOR columns

- Recognised by type name at introspection : a `cbor` domain over `bytea`, and postcbor's `cbor` type. Both use the same encoder ; postcbor's binary receive takes raw CBOR.
- Write : the item's bytes, sliced from the body when no structure sits inside, spliced otherwise (ADR 0012). Never re-encoded from the node tree.
- Reader changes (`query/node_cbor.go`) : each node keeps its byte range and a "structure inside" flag ; `itBadTag` keeps its content node.
- CBOR response : the stored bytes are copied as they are, structures or not. To check : whether a domain column comes with `bytea`'s OID in binary results ; if so the result encoder knows a CBOR column from the query, not the OID.
- JSON request touching a CBOR column (read, write, default selection) : refused at compile time (ADR 0013).
- Filters on CBOR columns : later, once postcbor's operators exist.

## Routes and well-known params

- `fn(req jsonb, body cbor)` : like `file bytea`, the whole body, `req.body` null. A JSON body is a 415. No `cbor[]`, no CBOR `stream_upload` (a part comes as `bytea[]` and is cast in the function).
- A `cbor` return : sent as Postgres produced it, `application/cbor`, whatever the `Accept`.
- A well-known param typed `cbor` : bound from its item's bytes (spliced when needed) ; from JSON, a 400. Declared with the existing well-known query syntax.

## Compressed requests (ADR 0014)

- `rejectEncodedBody` (`boot/mux.go`) becomes a decompressing reader for `gzip`, `deflate`, `zstd` ; anything else, or more than one coding, stays a 415.
- `http.max_body_size` bounds the decompressed bytes.
- The TypeScript codec gzips bodies above a threshold (default about 1 KB, configurable) through `CompressionStream`. It does not change its use of structures.
- Responses : still the proxy's job.

## Declared types (tsgen)

- A comment prefixed by its object kind is HUML (or JSON after `:` and `{`), as `route::` is : `column::`, `table::`. Without a prefix, a comment stays plain doc text.
- Fields : `comment` (the `/** */` text), `type` (any TS type expression, emitted as written), `from` (optional : `type` is then a name imported from that module). `table::` has only `comment` for now.
- Same declaration form for route `body` and `returns` (in `route::`) and for well-known params and result fields.
- Applies to `json`, `jsonb` and `cbor` columns ; undeclared stays as today (`unknown` for `cbor`).
- Config `tsgen.types."schema.table.column"` wins over a comment, with a warning.
- rel never checks a value against its declared type ; the docs point to Postgres `CHECK` constraints (`pg_jsonschema`, postcbor's equivalent).
- Docs : how to declare a type, inline and imported.
