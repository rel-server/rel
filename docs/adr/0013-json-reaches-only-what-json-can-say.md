# A JSON client reaches only what JSON can say

CBOR is a superset of what a JSON client can do : a CBOR client is accepted wherever JSON goes (ADR 0011), but a JSON client never reaches a value only CBOR can hold. A CBOR column in a JSON request is refused when the request is compiled, whether it is read, written, or part of a default selection ; a JSON body to a route taking a `cbor` argument is a 415, and a JSON value for a `cbor` well-known param a 400. A conversion would depend on the setup (postcbor can cast to `jsonb`, a bytea domain cannot), and an error can be lifted later while a shipped conversion cannot be withdrawn.

## Consequences

- A JSON client lists its fields on a table with a CBOR column.
- A client that wants JSON from a CBOR column asks for it explicitly, through postcbor's cast in its own query.
- A route returning `cbor` sends what Postgres produced as `application/cbor`, whatever the `Accept` : the route chose its type, as one returning `bytea` does.
- For a read, JSON means the response ; for a write, the request body. A CBOR request answered in JSON can write a CBOR column, not read it back.
- A read is refused from the resolved query, before any connection is taken ; a write when its value is encoded, inside the transaction, so nothing of it is committed.
- Only output is checked : a filter or a sort on a CBOR column is not refused.
