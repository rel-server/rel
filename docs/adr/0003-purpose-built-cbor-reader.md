# A purpose-built CBOR reader instead of a library's decode into Go values

Request bodies are only ever read, never needed as Go values, so a general decoder's maps, slices and boxed values are pure cost. rel reads the body in one pass into a flat array of items (`query/node_cbor.go`), each `Node` pointing at one item ; a string is copied and a number's text built only when something reads it, and the same pass validates the whole document. On a 50,000-row write body (3.7 MB), `fxamacker/cbor` took 38 ms and this reader 6.5 ms, with about 40 % fewer allocations than the JSON path.

## Consequences

Owning a decoder means owning its correctness : `fxamacker/cbor` stays as a test-only reference (`query/node_cbor_ref_test.go`), and every document, all 65,536 half floats, every truncation and over a million mutated random documents must give the same tree from both, or be refused by both.
