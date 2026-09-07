---
icon: material/package-variant
---

# Batching queries in one request

The request body can be a single query or an array of them — different shapes, different
relations, no shared root — and they all run inside one transaction:

```json
[
  { "query": { "relation": "properties", "schema": "hotel", "select": "*" },
    "data": { "id": 1, "name": "Marina Bay Grand Hotel" } },
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
