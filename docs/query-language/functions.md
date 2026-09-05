# Calling functions

A query can root on a function instead of a table, or join into one — `hotel.rooms_available`
is a table-valued function that returns a set of rooms:

```json
{
  "function": "rooms_available",
  "schema": "hotel",
  "arguments": { "property_id": 1, "on_date": ["2026-06-01"] },
  "select": "*"
}
```

`arguments` can be positional (an array, matching the function's own parameter order) or named
(an object, as above) — never both. A function-rooted or function-embedded node is always
read-only, even when its return type is an ordinary writable table: a write always targets the
underlying table directly, never "through" a function, since the function's own body might
filter or transform rows in ways a write should never silently bypass.

This is a different mechanism from [`call`](aggregates.md), which invokes a function as part of
an expression inside `select`/`where`, not as a query's root or a `join` target.
