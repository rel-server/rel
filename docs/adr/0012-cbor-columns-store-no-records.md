# CBOR columns store no records, and rel splices rather than re-encodes

A CBOR column (a `cbor` domain over `bytea`, or postcbor's `cbor` type) receives the bytes of the value's item, sliced from the request body when the item holds no structure. Records are a transport compression of `cbor-x` (ADR 0008) : stored, they would tie every reader of the column to `cbor-x`, and a stored value copied into a `structures=1` response would clash with the response's own structure ids. A definition is written at its first use, inside a record, so there is no standalone definition to prepend to a subtree that refers to one defined elsewhere.

An item that holds a structure is spliced : its bytes are copied, and only each structure is rewritten as a map of its names and values. It is never re-encoded from the node tree, which does not keep what the client wrote : tags 0 and 1 both become an instant, bignums and decimal fractions become text, `undefined` becomes null, and an unknown tag loses its content.

## Consequences

- Each node of the CBOR reader keeps its byte range, and the document the offsets of its structures ; an unknown tag keeps its content node.
- The reader accepts a map with a key that is not text and an unassigned simple value, which only a CBOR column takes.
- A bytea-domain column stores what the client wrote, records aside. postcbor normalizes whatever it receives on its own.
- A byte string sent to a CBOR column is a byte string value, never an already-encoded document ; tag 24 is stored as it is.
