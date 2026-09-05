# Operators reference

Every operator below is written the same way: a tag as the first array element, followed by
its operands. Most have a symbolic form and a word form — `["=", "a", "b"]` and `["eq", "a",
"b"]` compile identically; use whichever reads better in your query.

## Folding (variadic) operators

These accept two or more operands and fold left-to-right: `["+", 1, 2, 3]` is `1 + 2 + 3`;
`["<", "a", "b", "c"]` is `a < b and b < c`, not a three-way comparison.

| Symbol | Word form | Meaning |
|---|---|---|
| `and` | | boolean AND |
| `or` | | boolean OR |
| `+` | `add` | addition |
| `-` | `sub` | subtraction |
| `*` | `mul` | multiplication |
| `/` | `div` | division |
| `^` | `pow` | exponentiation |
| `%` | `mod` | modulo |
| <code>&#124;</code> | `bor` | bitwise OR |
| `&` | `band` | bitwise AND |
| `=` | `eq` | equals |
| `<>` / `!=` | `ne` | not equal |
| `<` | `lt` | less than |
| `>` | `gt` | greater than |
| `<=` | `lte` | less than or equal |
| `>=` | `gte` | greater than or equal |
| `is_distinct_from` / `!==` | | null-safe not-equal |
| `is_not_distinct_from` / `===` | | null-safe equal |
| `->` | `json_get` | jsonb key/index access, returns jsonb |
| `->>` | `json_get_text` | jsonb key/index access, returns text |
| `#>` | `json_path` | jsonb path access, returns jsonb |
| `#>>` | `json_path_text` | jsonb path access, returns text |
| `.` | `dot` | composite/embed field access — see [Selecting fields](selecting.md) |
| <code>&#124;&#124;</code> | `concat` | concatenation — does **not** coalesce nulls away |
| <code>&#124;&#124;?</code> | `concat_coalesce` | concatenation, coalescing each null operand to `''` first |
| `??` | `ifnull` | coalesce, JS-style alias |

## Unary operators

| Symbol | Word form | Meaning |
|---|---|---|
| `-` | `neg` | numeric negation |
| `~` | `bnot` | bitwise NOT |
| <code>&#124;/</code> | `sqrt` | square root |
| <code>&#124;&#124;/</code> | `cbrt` | cube root |
| | `not` | boolean NOT |
| | `is_null` / `is_not_null` | null test |
| | `is_true` / `is_not_true` | boolean test |
| | `is_false` / `is_not_false` | boolean test |

## Other binary operators

These take exactly two operands — folding across more than two doesn't make sense for them.

| Symbol | Word form | Meaning |
|---|---|---|
| `like` / `ilike` | | pattern match, case-sensitive/insensitive — disabled by default, see [Configuration](../configuration/index.md) |
| `~` / `~*` | `match` / `imatch` | regex match, case-sensitive/insensitive — disabled by default |
| `@@` | `matches_ts` | full-text search match — disabled by default |
| `::` | `cast` | type cast: `["::", "amount", "numeric"]` |
| `&&` | `overlap` | range/array overlap |
| `<->` | `distance` | distance operator (geometric types) |
| <code>-&#124;-</code> | `adjacent` | range adjacency |
| `<<` | `shl` | bitwise shift left |
| `>>` | `shr` | bitwise shift right |
| `@>` | `contains` | contains (array/range/jsonb) |
| `<@` | `contained_by` | contained by (array/range/jsonb) |
| `?` | `has_key` | jsonb has key |
| <code>?&#124;</code> | `has_any_key` | jsonb has any of these keys |
| `?&` | `has_all_keys` | jsonb has all of these keys |
| `&<` | `overlaps_or_left` | range overlaps or is left of |
| `&>` | `overlaps_or_right` | range overlaps or is right of |
| `?:` | `op_qcolon` | ternary-style conditional |

> `like`/`ilike`/`~`/`~*`/`@@` can be expensive on an unindexed or adversarial input — they're
> disabled by default and need an explicit config change to enable.

## Ranges, sets, and set membership

```json
["between", 100, "base_price", 500]
["not_between", 100, "base_price", 500]
["in", "status", "confirmed", "checked_in"]
["not_in", "status", "cancelled"]
["any", "=", "star_rating", ["arr", 4, 5]]
["all", "=", "star_rating", ["arr", 4, 5]]
```

`in`/`not_in`'s candidates are treated as literal values, not column references, even though
they're bare strings — the one place in the whole expression grammar where that's true.
`any`/`all` take an operator, a subject, and an array (or a to-many relation's column) to
compare every element against.

## Arrays and JSON

```json
["arr", 1, 2, 3]          // an array literal (also: "lst"/"list")
["index", "features", 1]  // 1-indexed, like Postgres — not 0-indexed
["slice", "features", 1, 2]
```

## String building

```json
["concat_ws", ", ", "city", "region"]
["coalesce", "phone", ["\"none\""]]
["format", "%s (%s)", "name", "star_rating"]
```

## Numeric and bigint literals

```json
["bigint", "9007199254740993"]
["numeric", "12345678901234567890.123456789"]
```

Plain JSON numbers lose precision past what a 64-bit float can represent exactly; wrap a
literal that needs to survive round-trip intact in one of these instead.
