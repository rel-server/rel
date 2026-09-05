# Filtering with `where`

`where` takes an expression that must evaluate to a boolean. Comparisons, boolean logic, and
arithmetic all use the same folded form — an operator followed by its operands:

```json
{ "where": ["and", [">=", "star_rating", 4], ["=", "chain_id", 3]] }
```

Folded operators (`and`, `or`, `+`, `-`, `*`, `/`, `=`, `<`, `>`, `<=`, `>=`, `<>`, ...) also
accept more than two operands and fold left-to-right — `["and", a, b, c]` is `a and b and c`;
`["<", 1, 2, 3]` is `1 < 2 and 2 < 3`. The full operator list is the [Operators
reference](operators.md); a few forms come up often enough to call out here:

```json
["between", 100, "base_price", 500]
["in", "status", "confirmed", "checked_in"]
["not_in", "star_rating", 1, 2]
["like", "name", ["%Grand%"]]
```

A bare string in an expression is always a column or alias reference; to filter on a literal
string, wrap it as a one-element array — `["%Grand%"]` above is the string `"%Grand%"`, not a
reference to a column named `%Grand%`. `like`/`ilike`/`~`/`~*` (pattern and regex matching) and
`@@` (full-text search) are worth knowing about but disabled by default — see
[Configuration](../configuration/index.md) to turn them on.

`any`/`all` compare a value against every element of an array or a to-many relation's column:

```json
["any", "=", "star_rating", ["arr", 4, 5]]
```
