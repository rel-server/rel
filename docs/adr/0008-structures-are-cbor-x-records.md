# Structures use cbor-x's record extension, written only for plan-node rows

A structure is a record's list of field names, written once and then referred to by number, so a row costs its values instead of its values and its names. rel uses the record extension of `cbor-x` (https://github.com/kriszyp/cbor-records), the TypeScript client's library, rather than a registered CBOR extension ; a generic CBOR decoder reads these as unknown tags. Responses use them only when asked (`Accept: application/cbor; structures=1`), and only for the rows of a query's plan nodes, whose field names are fixed whatever the row, so one id per node is enough and no row-by-row comparison is needed. A `jsonb` value has arbitrary keys that rarely repeat, where a definition per object would cost more than the map.

## Consequences

- Id 8191 is never used : `cbor-x` reads a tag as the structure numbered by its low 13 bits before it tests for a definition, and the definition tag `0xdfff` has the low bits of id 8191.
- Requests are read with the two forms `cbor-x` writes when records are on and `sequential` is off ; tag `0xdffe` and tag 105, its other forms, are refused as unknown tags.
