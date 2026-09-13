---
icon: material/filter
---

# Filtering with `where`

`where` takes an expression that must evaluate to a boolean. Comparisons, boolean logic, and
arithmetic all use the same folded form — an operator followed by its operands:

```json
{ "where": ["and", [">=", ["col", "star_rating"], 4], ["=", ["col", "chain_id"], 3]] }
```

Folded operators (`and`, `or`, `+`, `-`, `*`, `/`, `=`, `<`, `>`, `<=`, `>=`, `<>`, ...) also
accept more than two operands and fold left-to-right — `["and", a, b, c]` is `a and b and c`;
`["<", 1, 2, 3]` is `1 < 2 and 2 < 3`. The full operator list is the [Operators
reference](operators.md); a few forms come up often enough to call out here:

```json
["between", 100, ["col", "base_price"], 500]
["in", ["col", "status"], "confirmed", "checked_in"]
["not_in", ["col", "star_rating"], 1, 2]
["like", ["col", "name"], "%Grand%"]
```

### A bare string is a literal

A bare string in an expression is always a *literal* value, exactly like a bare number,
`true`, `false`, or `null` — `"%Grand%"` above is just the string `%Grand%`, never a reference.
To point at something instead, use:

- `["col", "name"]` — a real, physical column, asserted as such (errors clearly if `name`
  turns out to be an alias, a joined relation, or a computed field);
- `[".", "name"]` — anything in the current scope, no restriction: a column, an alias, a
  joined relation, or a [computed field](computed-fields.md#using-one).

Reach for `col` when you mean an actual column (most filters); reach for `.` for anything else,
or when you'd rather not assert. `like`/`ilike`/`~`/`~*` (pattern and regex matching) and `@@`
(full-text search) are worth knowing about but disabled by default — see
[Configuration](../configuration/index.md) to turn them on.

`any`/`all` compare a value against every element of an array or a to-many relation's column:

```json
["any", "=", ["col", "star_rating"], ["arr", 4, 5]]
```
