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
  [Computed fields](computed-fields.md) and [Configuration reference](../configuration/reference.md).

```json
{
  "relation": "properties", "schema": "hotel",
  "join": {
    "room_types": { "relation": "room_types", "schema": "hotel", "on": { "property_id": "id" } }
  },
  "select": {
    "name": "name",
    "room_type_count": ["agg", "count", [[".", "room_types", "id"]]],
    "budget_type_count": [
      "agg", "count", [[".", "room_types", "id"]],
      ["<", [".", "room_types", "base_price"], 150]
    ]
  }
}
```

> **Why:** `[".", "room_types", "id"]` — not the bare string `"room_types.id"` — because a joined
> alias's column is always reached through the `.` operator (see [Operators
> reference](operators.md)), never a dotted identifier string; there's no bare-string
> "alias.column" form in the JSON query grammar.

## Filtering which rows get aggregated

The fourth, optional element of `agg` filters which rows get aggregated, independent of the
query's own `where` — `budget_type_count` above counts only room types priced under 150, while
`room_type_count` still counts every room type, regardless of what the query's own `where` (if
any) does at the `properties` level.

## The function name

Both `call` and `agg` take a function name in the same form: either a bare, unqualified
string (resolved via the search path) or an explicit `{"schema": ..., "name": ...}` object.

A computed field (see [Computed fields](computed-fields.md)) is never a write target — see
[Writing data back](writing.md) for what makes a column writable.
