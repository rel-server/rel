---
icon: material/notebook
---

# Query shape reference

Every other page in this section teaches one piece of the query language by example. This page
is the other kind of reference: every field and every `Expression` form in one place, each
linked to the page that explains it. Skim this first to see the whole shape at a glance, or
come back to it once you know roughly what you're looking for and just need the exact name.

## The request body

`POST /rel`'s body is one of these, at the top level:

```ts
type Query = Relation | ComplexQuery | WellKnownQuery | Query[]
```

| Form | Meaning |
|---|---|
| A `Relation` | A read (see [Shaping a query](shaping.md)), or, wrapped in a `ComplexQuery`, a write. |
| A `WellKnownQuery` | A call to a named, pre-parsed query — see [Well-known queries](well-known-queries.md). |
| A `ComplexQuery` | Optionally write `data` back through `query` (a `Relation` or a `WellKnownQuery`), and/or shape the response beyond the plain selection — see [Writing data back](writing.md) and [Complex queries](complex-query.md). |
| `Query[]` | Several queries in one request, one transaction — see [Batching queries](batching.md). |

## `Relation`

Every field a query node can carry. `schema` through `limit` all apply the same way whether
the node is the query's root or a `join` target nested arbitrarily deep.

```ts
interface Relation {
  relation?: string
  function?: string
  schema?: string
  alias?: string
  on?: { [local_column: string]: string }
  arguments?: Expression[] | { [name: string]: Expression }
  where?: Expression
  write_mode?:
    | "insert" | "upsert" | "merge" | "merge-new" | "merge-update"
    | "update" | "deleteonly" | "readonly"
  on_conflict?: string | string[]
  insert_columns?: string[]
  update_columns?: string[]
  join?: { [alias: string]: Relation }
  select?: Expression
  distinct?: boolean
  distinct_on?: Expression[]
  order_by?: (
    | Expression
    | [direction: "asc" | "desc" | "asc-nulls-first" | "desc-nulls-last", Expression]
  )[]
  offset?: number
  limit?: number
}
```

| Field | Covered in |
|---|---|
| `relation` | [Shaping a query](shaping.md) — exactly one of `relation`/`function`. |
| `function` | [Calling functions](functions.md). |
| `schema` | [Shaping a query](shaping.md) — defaults to the search path. |
| `alias` | [Shaping a query](shaping.md) — self-references and self-joins. |
| `on` | [Joining and embedding relations](joining.md) — required on a `join` target. |
| `arguments` | [Calling functions](functions.md) — positional or named, only with `function`. |
| `where` | [Filtering with `where`](filtering.md). |
| `write_mode` | [Writing data back](writing.md). |
| `on_conflict` | [Writing data back](writing.md) — defaults to the primary key. |
| `insert_columns` | [Writing data back](writing.md). |
| `update_columns` | [Writing data back](writing.md). |
| `join` | [Joining and embedding relations](joining.md). |
| `select` | [Selecting fields](selecting.md) — defaults to `["full"]`. |
| `distinct` | [Distinctness](distinctness.md). |
| `distinct_on` | [Distinctness](distinctness.md) — must be a prefix of `order_by`. |
| `order_by` | [Ordering and pagination](ordering-pagination.md). |
| `offset` | [Ordering and pagination](ordering-pagination.md) — per parent row, inside a `join`. |
| `limit` | [Ordering and pagination](ordering-pagination.md) — per parent row, inside a `join`. |

## `WellKnownQuery` and `ComplexQuery`

```ts
interface WellKnownQuery {
  wellknown: string
  params?: any
}

interface ComplexQuery {
  query: Relation | WellKnownQuery
  data?: any // shaped like `query`'s own `select` ; write, if present, else a read
  returns?: "none" | "results"
  count?: boolean
  stats?: boolean
  query_plan?: boolean
  sql?: boolean
  rollback?: boolean
}
```

| Type | Field | Covered in |
|---|---|---|
| `WellKnownQuery` | `wellknown` | [Well-known queries](well-known-queries.md). |
| `WellKnownQuery` | `params` | [Well-known queries ## Declaring and using parameters](well-known-queries.md#declaring-and-using-parameters). |
| `ComplexQuery` | `query` | [Writing data back](writing.md). |
| `ComplexQuery` | `data` | [Writing data back](writing.md) — omit it entirely for a read. |
| `ComplexQuery` | `returns`, `count`, `stats`, `query_plan`, `sql`, `rollback` | [Complex queries](complex-query.md). |

## `Expression`

Every legal value for `where`, `select`, `order_by`, `distinct_on`, and function `arguments`.
Operators (`FoldedOperator`/`BinaryOperator`/`UnaryOperator`) get their own exhaustive table on
the [Operators reference](operators.md) — this table covers every other shape `Expression` can
take.

| Form | Meaning | Covered in |
|---|---|---|
| `null` / `true` / `false` / a number | A literal value. | [Filtering with `where`](filtering.md). |
| `"*"` | Every field of the current relation, plus every join alias. | [Selecting fields](selecting.md). |
| a bare string | A column, alias, or computed field reference. | [Filtering with `where`](filtering.md), [Computed fields](computed-fields.md). |
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
| <code>["agg"&#124;"aggregate", identifier, arguments, filter?]</code> | Aggregate an incoming relation's column. | [Aggregates](aggregates.md). |
| `["call", identifier, ...arguments]` | Call an allowed function explicitly — needed for a cross-schema computed field, or any other function call. | [Computed fields](computed-fields.md). |
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
string (resolved via the search path) or a schema-qualified reference:

```ts
type FunctionIdentifier = string | { schema: string, name: string }
```

See [Computed fields](computed-fields.md) and [Aggregates](aggregates.md).
