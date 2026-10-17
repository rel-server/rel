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
    "name": ["col", "name"],
    "room_type_count": ["agg", "count", [[".", "room_types", "id"]]],
    "budget_type_count": [
      "agg", "count", [[".", "room_types", "id"]],
      { "filter": ["<", [".", "room_types", "base_price"], 150] }
    ]
  }
}
```

> **Why:** `[".", "room_types", "id"]` — not the bare string `"room_types.id"`, and not
> `["col", "room_types", "id"]` — because a joined alias is resolved from scope with `.` (`room_types`
> isn't a real column, so `col` won't reach it), then hopped into by chaining another operand onto
> the same `.`; there's no bare-string "alias.column" form in the JSON query grammar.

## Options

The fourth, optional element of `agg` is an object of options, each optional:

| Option | Meaning |
|---|---|
| `filter` | Only the rows this condition holds for are aggregated, independent of the query's own `where`. `budget_type_count` above counts only room types priced under 150, while `room_type_count` still counts every room type. |
| `distinct` | `true` aggregates each distinct combination of the arguments once: `count(distinct x)`. |
| `order_by` | The order the rows are fed to the aggregate, in the same form as a query's [`order_by`](ordering-pagination.md): `array_agg(x order by y)`. With `distinct`, every term must also be one of the arguments (Postgres' rule). |

```json
"names": ["agg", "string_agg", [["::", [".", "room_types", "name"], "text"], ", "],
          { "order_by": [[".", "room_types", "name"]] }]
```

## The function name

Both `call` and `agg` take a function name in the same form: either a bare, unqualified
string (resolved via the search path) or an explicit `{"schema": ..., "name": ...}` object.

Many functions exist in several versions that differ by argument type: `sum`, `avg`, `min`,
`max` and `abs` are all overloaded. rel does not choose between them: it checks that the name
exists, is an aggregate (for `agg`) or a plain function (for `call`), is allowed, and that some
version takes that many arguments, then Postgres picks the version from the actual argument
types. A literal has no type of its own, so give it one when the choice would otherwise be open:
`["::", 5, "numeric"]`. When Postgres cannot choose, or no version fits, the request fails with
`400` and a `PG_UNDEFINED_FUNCTION`, `PG_AMBIGUOUS_FUNCTION` or `PG_INDETERMINATE_DATATYPE` code
carrying Postgres' own message.

The result of `int8` and `numeric` aggregates (`sum`, `avg`, ...) is a JSON number, like `count`.

A computed field (see [Computed fields](computed-fields.md)) is never a write target — see
[Writing data back](writing.md) for what makes a column writable.
