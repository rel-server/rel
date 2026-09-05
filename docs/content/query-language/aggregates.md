---
icon: lucide/layers
---

# Computed fields and aggregates

`call` invokes an allowed function directly as part of an expression:

```json
{ "nights": ["call", "booking_nights", "$self"] }
```

`agg`/`aggregate` aggregates a joined, incoming relation's column into the parent — the target
column must belong to a relation joined as incoming (see [Joining and embedding
relations](joining.md)), since aggregating only makes sense over rows the current row actually
owns:

```json
{
  "relation": "properties", "schema": "hotel",
  "join": {
    "reviews": { "relation": "reviews", "schema": "hotel", "on": { "property_id": "id" } }
  },
  "select": {
    "name": "name",
    "average_rating": ["agg", "avg", ["reviews.rating"]],
    "review_count": ["agg", "count", ["reviews.id"]],
    "five_star_count": [
      "agg", "count", ["reviews.id"], ["=", "reviews.rating", 5]
    ]
  }
}
```

The fourth, optional element of `agg` filters which rows get aggregated, independent of the
query's own `where`. Both `call` and `agg` take a function name that's either a bare,
unqualified string (resolved via the search path) or an explicit `{"schema": ..., "name":
...}` object — and both are checked against the configured function allowlist before they're
allowed to run at all; see [Configuration](../configuration/index.md).

A computed column is never a write target — see [Writing data back](writing.md) for what makes
a column writable.
