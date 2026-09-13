---
icon: material/cursor-default-click
---

# Selecting fields

`select` shapes the response. Left unset, it defaults to `["*"]` — every column of the
relation plus every joined relation. The building blocks:

| Form | Meaning |
|---|---|
| `["col", "name"]` | one *real, physical* column, by name |
| `[".", "name"]` | anything in scope by name — a column, alias, joined relation, or [computed field](computed-fields.md) |
| `["*"]` | every physical column of this relation, plus every joined relation |
| `["*~"]` | every physical column of this relation, no joins |
| `["*", ["id"]]` / `["*~", ["id"]]` | all columns except the ones named |
| `["*", {"nights": "booking_nights"}]` | all columns plus computed keys |
| `["*", ["id"], {"id": ["call", "..."]}]` | omit some, add/override others, both at once |
| `{"id": ["col", "id"], "name": ["col", "name"]}` | an explicit object literal — only these keys |

A bare JSON string is always a *literal* value now, never a reference — see [Filtering with
`where`](filtering.md#a-bare-string-is-a-literal). To point at something, use `col` (asserts a
real column; fails clearly on a typo'd alias) or `.` (looks up anything in scope, no
restriction). Object literals nest arbitrarily and can mix real columns, joined relations, and
computed values in one shape:

```json
{
  "select": {
    "name": ["col", "name"],
    "star_rating": ["col", "star_rating"],
    "chain_name": [".", [".", "chain"], "name"],
    "room_types": ["col", "room_types"]
  }
}
```

## The dot-chain form

`.` is the general-purpose "look this up" building block. With one operand, it's a plain scope
lookup — `[".", "name"]` resolves `name` against the current relation exactly like a bare
string used to, no restriction to real columns. With two or more, it's a chain: the first
operand is a full expression producing a base value, and every operand after that is always a
bare hop-name (never re-parsed as a literal), stepping one field into that base —
`[".", [".", "chain"], "name"]` pulls one field from a joined relation flat into this level,
instead of nesting it as its own object. `chain` still has to be declared in `join` for this to
resolve — see [Joining and embedding relations](joining.md). When the base is itself a real
column of a to-one relation type (an FK column doubling as the join target), `["col", "..."]`
can stand in as the base instead of `[".", "..."]` — see
[Computed fields](computed-fields.md#using-one) for an example.

`*`/`*~` never pull in a [computed field](computed-fields.md) automatically — name it
explicitly, by its bare name via `.`, to include it. Naming one in the except-list is an
error: it was never included in the first place, so there's nothing to except.

## `get`, `set`, and `col`

These three control read/write visibility and defaults on one column independently, and — like
`col` on its own — always require a real, physical column, never an alias or computed field:

- `["get", "notes", "n/a"]` reads `notes` (falling back to `"n/a"` when null), but never
  accepts it on a write.
- `["set", "notes"]` accepts `notes` on a write, but never reads it back.
- `["col", "notes", "n/a", "pending"]` reads with one default (`"n/a"`) and writes with
  another (`"pending"`) — `col` with no default arguments is just a plain column reference.

See [Writing data back](writing.md) for how a column's presence here decides whether it's
writable at all.
