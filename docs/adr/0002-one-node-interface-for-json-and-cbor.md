# One `Node` interface over JSON and CBOR, CBOR never converted to JSON text

The query parser and the write encoders read a `Node` interface, implemented by a wrapper over sonic's JSON tree and by the CBOR reader, so neither the parser nor the query compiler is duplicated. CBOR is not converted to JSON text first : that would lose byte strings, tags and non-text map keys, and cost a conversion of every write payload. A value only becomes JSON text where Postgres receives JSON (`json`/`jsonb` columns, text-mode columns given a map or array, a `$param` cast to `json`/`jsonb`).

## Consequences

The interface is not free on the JSON path : measured against the parser before it, 5 to 7 % more time to parse a query body, 20 to 25 % more allocations (one `[]Node` per array, one closure per object), and about 1 % more on parse, resolve and compile together.
