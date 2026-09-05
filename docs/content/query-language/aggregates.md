---
icon: material/sigma
---

# Aggregates

`agg`/`aggregate` aggregates a joined, incoming relation's column into the parent — necessary
conditions:

- the target column must belong to a relation joined as **incoming** (see
  [Joining and embedding relations](joining.md)), since aggregating only makes sense over rows
  the current row actually owns;
- the aggregate function name is checked against the same function allowlist `call` is — see
  [Computed fields](computed-fields.md) and [Configuration](../configuration/index.md).

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

## Filtering which rows get aggregated

The fourth, optional element of `agg` filters which rows get aggregated, independent of the
query's own `where` — `five_star_count` above counts only reviews with `rating = 5`, while
`review_count` still counts every review, regardless of what the query's own `where` (if any)
does at the `properties` level.

## The function name

Both `call` and `agg` take a function name in the same form: either a bare, unqualified
string (resolved via the search path) or an explicit `{"schema": ..., "name": ...}` object.

A computed field (see [Computed fields](computed-fields.md)) is never a write target — see
[Writing data back](writing.md) for what makes a column writable.
