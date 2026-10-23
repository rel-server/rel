---
icon: material/package-variant
---

# Batching queries in one request

The request body can be a single query or an array of them — different shapes, different
relations, no shared root — and they all run inside one transaction:

```json
[
  { "query": { "relation": "properties", "schema": "hotel", "select": ["*~"] },
    "data": [{ "id": 1, "name": "Marina Bay Grand Hotel" }] },
  { "wellknown": "top_rated_properties", "params": { "min_rating": 4 } }
]
```

Mixing a plain `Relation` with a `WellKnownQuery` call (`{"wellknown": "...", "params": {...}}`)
in the same batch is fine — see [Well-known queries](well-known-queries.md) for how those are
registered and typed. If any item in the batch fails, the whole batch rolls back.

## Response shape

A batch of more than one item comes back as a top-level JSON array, one entry per request item,
in request order — each entry is exactly what that item would have returned on its own: a bare
row array/object for a plain query, or an envelope object (`result`/`count`/`stats`/`sql`/
`query_plan`) for a [complex query](complex-query.md). A single-item request — batched or not —
never gets this extra array wrapper; `Query[]` with exactly one element responds the same way a
bare `Query` would.

## When a request fails

The first `http.response_buffer` bytes of a `/rel` response (64 KiB by default) are held back until
the transaction has committed. A failure while a response that small is produced — a row that
errors after the first one, a deferred constraint refused at commit — is answered with the usual
error response, and nothing is written to the database.

A longer response starts streaming once the buffer is full. If a failure happens after that, the
`200` has already been sent: the body stops short, the transaction is rolled back, and the failure
is logged on the server. A client that reads a response to the end (a closed JSON array, a CBOR
break) can tell a complete response from a cut-short one. Set `http.response_buffer` to `0` to
stream from the first byte, or higher to cover bigger responses at the cost of that much memory per
request in flight.
