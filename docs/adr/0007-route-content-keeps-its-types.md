# A route function's value keeps its column types in CBOR

A route function may return any type with a value, not only `json`/`jsonb`. When the client accepts CBOR, the value is read in Postgres' binary format and written by the same encoder as query results, so it has one shape on the wire wherever it comes from : a `numeric` is tag 4 from a query or from a function returning `numeric`. This is why returning the typed value is better than building `jsonb` by hand, where numbers lose their column types.

## Consequences

The content of a full-control function (its second `OUT` column) is the exception : it is read in the same `select` as the envelope, which is JSON text, so it takes the JSON-then-convert path.
