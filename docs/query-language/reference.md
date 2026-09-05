# Query shape reference

Every other page in this section teaches one piece of the query language by example. This page
is the other kind of reference: every field and every `Expression` form in one place, each
linked to the page that explains it. Skim this first to see the whole shape at a glance, or
come back to it once you know roughly what you're looking for and just need the exact name.

## The request body

`POST /rel`'s body is one of these, at the top level:

| Form | Meaning |
|---|---|
| A `Relation` | A read (see [Shaping a query](shaping.md)), or, wrapped in a `WriteQuery`, a write. |
| A `WellKnownQuery` (`{wellknown, params?}`) | A call to a named, pre-parsed query — see [Well-known queries](well-known-queries.md). |
| A `WriteQuery` (`{query, data}`) | Write `data` back through `query` (a `Relation` or a `WellKnownQuery`) — see [Writing data back](writing.md). |
| An array of any of the above | Several queries in one request, one transaction — see [Batching queries](batching.md). |

## `Relation`

Every field a query node can carry. `schema` through `limit` all apply the same way whether
the node is the query's root or a `join` target nested arbitrarily deep.

| Field | Type | Covered in |
|---|---|---|
| `relation` | `string` | [Shaping a query](shaping.md) — exactly one of `relation`/`function`. |
| `function` | `string` | [Calling functions](functions.md). |
| `schema` | `string` | [Shaping a query](shaping.md) — defaults to the search path. |
| `alias` | `string` | [Shaping a query](shaping.md) — self-references and self-joins. |
| `on` | `{[local_column]: string}` | [Joining and embedding relations](joining.md) — required on a `join` target. |
| `arguments` | `Expression[]` or `{[name]: Expression}` | [Calling functions](functions.md) — positional or named, only with `function`. |
| `where` | `Expression` | [Filtering with `where`](filtering.md). |
| `write_mode` | one of `insert`/`upsert`/`merge`/`merge-new`/`merge-update`/`update`/`deleteonly`/`readonly` | [Writing data back](writing.md). |
| `on_conflict` | `string` or `string[]` | [Writing data back](writing.md) — defaults to the primary key. |
| `insert_columns` | `string[]` | [Writing data back](writing.md). |
| `update_columns` | `string[]` | [Writing data back](writing.md). |
| `join` | `{[alias]: Relation}` | [Joining and embedding relations](joining.md). |
| `select` | `Expression` | [Selecting fields](selecting.md) — defaults to `["full"]`. |
| `distinct` | `boolean` | [Ordering, distinctness, and pagination](ordering-pagination.md). |
| `distinct_on` | `Expression[]` | [Ordering, distinctness, and pagination](ordering-pagination.md). |
| `order_by` | <code>(Expression &#124; [direction, Expression])[]</code> | [Ordering, distinctness, and pagination](ordering-pagination.md). |
| `offset` | `number` | [Ordering, distinctness, and pagination](ordering-pagination.md) — per parent row, inside a `join`. |
| `limit` | `number` | [Ordering, distinctness, and pagination](ordering-pagination.md) — per parent row, inside a `join`. |

## `WellKnownQuery` and `WriteQuery`

| Type | Field | Type | Covered in |
|---|---|---|---|
| `WellKnownQuery` | `wellknown` | `string` | [Well-known queries](well-known-queries.md). |
| `WellKnownQuery` | `params` | `any` | [Well-known queries ## Declaring and using parameters](well-known-queries.md#declaring-and-using-parameters). |
| `WriteQuery` | `query` | <code>Relation &#124; WellKnownQuery</code> | [Writing data back](writing.md). |
| `WriteQuery` | `data` | `any`, shaped like `query`'s own `select` | [Writing data back](writing.md). |

## `Expression`

Every legal value for `where`, `select`, `order_by`, `distinct_on`, and function `arguments`.
Operators (`FoldedOperator`/`BinaryOperator`/`UnaryOperator`) get their own exhaustive table on
the [Operators reference](operators.md) — this table covers every other shape `Expression` can
take.

| Form | Meaning | Covered in |
|---|---|---|
| `null` / `true` / `false` / a number | A literal value. | [Filtering with `where`](filtering.md). |
| `"*"` | Every field of the current relation, plus every join alias. | [Selecting fields](selecting.md). |
| a bare string | A column or alias reference. | [Filtering with `where`](filtering.md). |
| `[string]` | A one-element array — a string *literal*, not a reference. | [Filtering with `where`](filtering.md). |
| `[UnaryOperator, Expression]` | A unary operator call. | [Operators reference](operators.md). |
| `[BinaryOperator, left, right]` | A two-operand operator call. | [Operators reference](operators.md). |
| `[FoldedOperator, ...Expression[]]` | A variadic, left-folding operator call. | [Operators reference](operators.md). |
| <code>["between"&#124;"not_between", min, exp, max]</code> | Range test. | [Operators reference](operators.md). |
| <code>["bigint"&#124;"numeric", value: string]</code> | A precise numeric literal, past `float64`'s range. | [Operators reference](operators.md). |
| <code>["in"&#124;"not_in", subject, ...candidates]</code> | Set membership; candidates are always literals. | [Operators reference](operators.md). |
| <code>["any"&#124;"all", op, subject, array]</code> | Compare against every element of an array/to-many column. | [Operators reference](operators.md). |
| `["concat_ws", separator, ...Expression[]]` | Join strings with a separator. | [Operators reference](operators.md). |
| `["coalesce", ...Expression[]]` | First non-null operand. | [Operators reference](operators.md). |
| `["format", format: string, ...Expression[]]` | `printf`-style string building. | [Operators reference](operators.md). |
| <code>["agg"&#124;"aggregate", identifier, arguments, filter?]</code> | Aggregate an incoming relation's column. | [Computed fields and aggregates](aggregates.md). |
| `["call", identifier, ...arguments]` | Call an allowed function. | [Computed fields and aggregates](aggregates.md). |
| `{[name]: Expression}` | An object literal — a select shape. | [Selecting fields](selecting.md). |
| `["own"]` / `["full"]` | All columns / all columns plus joins. | [Selecting fields](selecting.md). |
| <code>["own_except"&#124;"full_except", except]</code> | All columns except the ones named. | [Selecting fields](selecting.md). |
| <code>["own_and"&#124;"full_and", and]</code> | All columns plus computed keys. | [Selecting fields](selecting.md). |
| <code>["own_except_and"&#124;"full_except_and", except, and]</code> | Both of the above at once. | [Selecting fields](selecting.md). |
| <code>["arr"&#124;"array", ...Expression[]]</code> | An array literal. | [Operators reference](operators.md). |
| `["index", array, index]` | 1-indexed array access. | [Operators reference](operators.md). |
| `["slice", array, from, to]` | 1-indexed array slice. | [Operators reference](operators.md). |
| <code>["lst"&#124;"list", ...Expression[]]</code> | A list literal — synonym for `arr`/`array`. | [Operators reference](operators.md). |
| `["get", column, default?]` | Read-only column reference. | [Selecting fields](selecting.md). |
| `["set", column, default?]` | Write-only column reference. | [Selecting fields](selecting.md). |
| `["get-set", column, default_get?, default_set?]` | Independent read/write defaults on one column. | [Selecting fields](selecting.md). |
| `["$param", name, cast?]` | A well-known query's own declared parameter. | [Well-known queries](well-known-queries.md#declaring-and-using-parameters). |

`FunctionIdentifier` — `call`'s and `agg`'s first argument — is either a bare, unqualified
string (resolved via the search path) or `{schema, name}` (a schema-qualified reference); see
[Computed fields and aggregates](aggregates.md).
