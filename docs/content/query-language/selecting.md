---
icon: material/cursor-default-click
---

# Selecting fields

`select` shapes the response. Left unset, it defaults to `["full"]` — every column of the
relation plus every joined relation. The building blocks:

| Form | Meaning |
|---|---|
| `"name"` | one column, alias, or [computed field](computed-fields.md), by its own name |
| `["own"]` | every physical column of this relation, no joins |
| `["full"]` | every physical column, plus every joined relation |
| `["own_except", ["id"]]` / `["full_except", [...]]` | all columns except the ones named |
| `["own_and", {"nights": "booking_nights"}]` | all columns plus computed keys |
| `["own_except_and", ["id"], {"id": ["call", "..."]}]` | omit some, add/override others |
| `{"id": "id", "name": "name"}` | an explicit object literal — only these keys |

Object literals nest arbitrarily and can mix real columns, joined relations, and computed
values in one shape:

```json
{
  "select": {
    "name": "name",
    "star_rating": "star_rating",
    "chain_name": [".", "chain", "name"],
    "room_types": "room_types"
  }
}
```

## The dot-chain form

`[".", "chain", "name"]` pulls one field from a joined relation flat into this level, instead
of nesting it as its own object. `chain` still has to be declared in `join` for this to
resolve — see [Joining and embedding relations](joining.md).

`own`/`full` never pull in a [computed field](computed-fields.md) automatically — name it
explicitly, by its bare name, to include it. Naming one in `own_except`/`full_except` is an
error: it was never included in the first place, so there's nothing to except.

## `get`, `set`, and `get-set`

These three control read/write visibility and defaults on one column independently:

- `["get", "notes", "n/a"]` reads `notes` (falling back to `"n/a"` when null), but never
  accepts it on a write.
- `["set", "notes"]` accepts `notes` on a write, but never reads it back.
- `["get-set", "notes", "n/a", "pending"]` reads with one default (`"n/a"`) and writes with
  another (`"pending"`).

See [Writing data back](writing.md) for how a column's presence here decides whether it's
writable at all.
