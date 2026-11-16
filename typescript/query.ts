/*!
This file specifies the shape of the JSON queries that rel is to understand.
*/

/**
 A query is one query, or a sequence run in order in one transaction ; any failure fails the whole transaction.
 For more complex needs, use a function instead.
 */
export type Query = ComplexQuery | RelationQuery | WellKnownQuery | readonly Query[]

/* Call a query registered in rel : like a view, but both readable and writable. */
export interface WellKnownQuery {
  wellknown: string
  params?: unknown
}

/**
 Wraps a `RelationQuery`/`WellKnownQuery`, optionally writing to it and/or shaping the response beyond the plain
 selection.
 */
export interface ComplexQuery {
  query: RelationQuery | WellKnownQuery

  /**
  The data to modify the database with. It must conform to the shape of the query. Present : `query` is written.
  Absent : `query` is read exactly as a bare `RelationQuery`/`WellKnownQuery` would be.
  */
  data?: unknown

  /** "none" skips streaming `result` back ; a write additionally skips the read-back `SELECT` entirely. Defaults to "results". */
  returns?: "none" | "results"

  /** Read-only. Reports the total row count of the query's own where/joins, ignoring its own limit/offset. */
  count?: boolean

  /** Write-only. Reports rows submitted/inserted/updated/deleted per writable node touched. */
  stats?: boolean

  /** The query plan(s) of every statement this request runs. Mutually exclusive with `stats` on a write. */
  query_plan?: boolean

  /** The already-compiled SQL text of every statement this request runs. */
  sql?: boolean

  /** Runs this item inside its own SAVEPOINT, rolled back right after execution — the rest of the request is unaffected. */
  rollback?: boolean
}

/** One writable node's row counts from a `stats` request. */
export interface Stat {
  /** Join-alias path from the root down to this node ; `[]` for the root relation itself. */
  path: string[]
  /** Fully-qualified relation name, "schema.table". */
  table: string
  submitted: number
  inserted: number
  updated: number
  deleted: number
}

/** One node's query plan(s) from a `query_plan` request. */
export interface PlanResult {
  /** Join-alias path from the root down to this node ; `[]` for the root relation itself. */
  path: string[]
  select?: unknown
  insert?: unknown
  update?: unknown
  upsert?: unknown
  delete?: unknown
}

/** One node's compiled SQL statement text(s) from a `sql` request. */
export interface SqlResult {
  /** Join-alias path from the root down to this node ; `[]` for the root relation itself. */
  path: string[]
  select?: string
  insert?: string
  update?: string
  upsert?: string
  delete?: string
}

/** The envelope shape a `ComplexQuery` response takes once any flag is set. */
export interface ComplexResult {
  /** The plain selection ; absent iff `returns == "none"`. */
  result?: unknown
  /** Present iff `count == true`. */
  count?: number
  /** Present iff `count == true` ; echoes the query's own offset (0 if unset). */
  offset?: number
  /** Present iff `count == true` ; echoes the query's own limit (absent if unset). */
  limit?: number
  stats?: Stat[]
  query_plan?: PlanResult[]
  sql?: SqlResult[]
}

export type Keys<T> = Extract<keyof T, string>

export interface RelationQuery<
  Rel extends object = { [name: string]: unknown },
  Join extends { [name: string]: RelationQuery } = { [name: string]: RelationQuery },
  // func()/call() narrow this against the function's declared arguments rather than Rel's columns.
  FunctionArguments = readonly Expression<Keys<Rel>>[] | { [name: string]: Expression<Keys<Rel>> },
> {
  // Identifying fields for the relation : exactly one of `relation` or
  // `function` must be given ; supplying both, or neither, is an error.
  /** The name of a table or view to query. Mutually exclusive with `function`. */
  relation?: string

  /**
    The name of a function to call, instead of querying a table/view directly. Mutually exclusive with `relation`.
    A function node is never writable, even if it returns a writable table's rows.
  */
  function?: string

  /** If not provided, the relation/function will be looked for in the search path. */
  schema?: string

  // Alias for this relation, usable by children/sibling expressions ; distinct from `join`'s own keys, which the parent uses to select.
  alias?: string

  /**
   Columns to join on : keys are this relation's columns, values are the enclosing query's columns. Mandatory on joined relations.
   Limited to FK-backed columns, or unique-backed distant columns ; this relation's own `on` columns must also be indexed, or the query is rejected.
  */
  on?: { [local_column: string]: string }

  /**
   Client-side only : one of the schema's `Relationships` shortcut strings, expanded into `on`/`relation`/`schema`
   and never sent.
  */
  shortcut?: string

  /** Positional or named arguments to `function`. Only valid alongside `function` ; an error alongside `relation`. */
  arguments?: FunctionArguments

  /** Restrict the rows produced by this relation according to a condition. */
  where?: Expression<Keys<Rel> | Keys<Join>>

  /**
    How this relation's data is handled when POSTing.

    Delete-bearing modes (`merge`, `merge-new`, `merge-update`, `deleteonly`) are only valid on an incoming
    relation (rows exclusively owned by the parent through the join) ; elsewhere, including the root, they are rejected.

    `update` and `delete` are the two where-driven modes : root only, `where` required, rows chosen by the `where`
    rather than by the payload. Joins under them can only be `readonly`, and `delete` takes no join at all.

    Defaults : `insert` for the root relation, `merge` for an incoming subquery, `upsert` for an outgoing one.
  */
  write_mode?: /** Delete rows not in the payload that match the where condition as well as foreign key relationships with a parent row if this query is a subquery, insert new ones and update existing ones.
     *
     This is the default for an incoming subquery. Not valid on an outgoing relation (see above).
     */
    | "merge"
    /** Do NOT update this particular table. This disables its own subqueries as well. */
    | "readonly"
    /** Insert new rows and do not touch the already exiting ones. This is the default for the outermost resource. */
    | "insert"
    /** Insert or update (... on conflict do update), but do not delete non-matching rows of the where condition. This is the default for an outgoing subquery. */
    | "upsert"
    /** Delete and insert but do not update rows matching those of the payload. Not valid on an outgoing relation (see above). */
    | "merge-new"
    /** Delete and update, but do not insert rows of the payload with no equivalent on the unique conditions. Not valid on an outgoing relation (see above). */
    | "merge-update"
    /** Only update the rows whose identity is in the payload ; ignore new ones and don't delete non-matching rows. */
    | "update-existing"
    /** Apply `data`, a single patch object, to every row the root's `where` selects. Root only ; `where` is required. */
    | "update"
    /** Delete every row the root's `where` selects ; takes no `data`, no join, and returns an empty selection. Root only ; `where` is required. */
    | "delete"
    /** Delete only rows matching the where condition and not in data. Not valid on an outgoing relation (see above). */
    | "deleteonly"

  /** The UNIQUE/PRIMARY KEY constraint (name, or its columns) used for conflict resolution on insert/update/merge.
   Defaults to the PRIMARY KEY only ; a UNIQUE constraint is never picked automatically when there is none. */
  on_conflict?: string | readonly string[]

  /** Limits which columns are inserted from the payload ; also applies to updates unless `update_columns` is given. Defaults to all writable columns. */
  insert_columns?: readonly string[]

  /** Columns to update if a row already exists. Defaults to all columns from the payload. */
  update_columns?: readonly string[]

  /** Related tables to embed, keyed by the alias used in `select`. To-many joins produce an array, to-one joins an object. */
  join?: Join

  /**
   The shape returned by the select. Defaults to `select *` plus every `join` embed ; an error is raised if a join
   alias then conflicts with a column name. An expression that doesn't produce an object yields one bare value per row.
  */
  select?: Expression<Keys<Rel> | Keys<Join>>

  distinct?: boolean

  distinct_on?: readonly Expression[]

  // if not supplied, "asc" is the default, just like in SQL
  order_by?: readonly (
    | Expression
    // asc and desc are nulls last by default
    | readonly ["asc" | "desc" | "asc-nulls-first" | "desc-nulls-last", Expression]
  )[]

  // The following two clauses are SQL's clauses. When used in a subquery, applies them for each parent-row
  offset?: number
  limit?: number

  /**
   Getters/methods set as the prototype of every row this node produces. Client-side only. When mixing a `get` with
   a plain method, give each an explicit return type.
  */
  proto?: object
}

export type UnaryOperator =
  | "-"
  | "neg"
  | "not"
  | "~"
  | "bnot"
  | "is_null"
  | "is_true"
  | "is_false"
  | "is_not_null"
  | "is_not_true"
  | "is_not_false"
  | "|/"
  | "sqrt" // square root
  | "||/"
  | "cbrt" // cube root

/**
 These operators are binary operators but that can be applied over a long list starting from the left and two by two
 ["-", 4, 3, 2, 1] -> ["-", ["-", ["-", 4, 3], 2], 1]
 Boolean operators are treated as and
 ["<", 1, 2, 3, 4] -> ["and", ["<", 1, 2], ["<", 2, 3], ["<", 3, 4]]
 */
export type FoldedOperator =
  | "and"
  | "or"
  | "+"
  | "add"
  | "-"
  | "sub"
  | "*"
  | "mul"
  | "/"
  | "div"
  | "^"
  | "pow"
  | "%"
  | "mod"
  | "|"
  | "bor"
  | "&"
  | "band"
  | "->"
  | "json_get"
  | "->>"
  | "json_get_text"
  | "#>"
  | "json_path"
  | "#>>"
  | "json_path_text"
  | "."
  | "dot"
  | "||"
  | "concat" // does NOT coalesce
  | "||?"
  | "concat_coalesce" // coalescing ||, not a postgres operator, synonymous with concat : coalesces individual operands with ''
  | "??"
  | "ifnull" // alias for coalesce, borrowed from javascript
  | "<="
  | "lte"
  | ">="
  | "gte"
  | "<"
  | "lt"
  | ">"
  | "gt"
  | "="
  | "eq"
  | "<>"
  | "!="
  | "ne"
  | "is_distinct_from"
  | "!==" // javascript alias
  | "is_not_distinct_from"
  | "===" // javascript alias

/**
 Here are all binary for who folding makes little sense
 */
export type BinaryOperator =
  | "like" // warning : need configuration as they can be abused for DDoS attacks
  | "ilike" // warning : need configuration as they can be abused for DDoS attacks
  | "~"
  | "match" // warning : need configuration as they can be abused for DDoS attacks
  | "~*"
  | "imatch" // warning : need configuration as they can be abused for DDoS attacks
  | "::"
  | "cast" // type cast
  | "&&"
  | "overlap"
  | "<->"
  | "distance"
  | "-|-"
  | "adjacent"
  | "<<"
  | "shl"
  | ">>"
  | "shr"
  | "@>"
  | "contains"
  | "<@"
  | "contained_by"
  | "?"
  | "has_key"
  | "?|"
  | "has_any_key"
  | "&<"
  | "overlaps_or_left"
  | "&>"
  | "overlaps_or_right"
  | "?&"
  | "has_all_keys"
  | "?:"
  | "op_qcolon"
  | "@@"
  | "matches_ts"

/** Names a function/aggregate for "call"/"agg" — a bare (unqualified) name, or an explicit { schema, name }.
Never a combined "schema.name" string : a quoted Postgres identifier can itself contain a literal dot. */
export type FunctionIdentifier = string | { schema: string; name: string }

// Every tuple/array variant below is `readonly` so `as const` values can be passed in.
/** The options of an `"agg"` expression, each optional. */
export interface AggOptions<K extends string = string> {
  /** Only the rows this condition holds for are aggregated (SQL's `filter (where ...)`). */
  filter?: Expression<K>
  /** `count(distinct x)` : aggregate each distinct combination of the arguments once. */
  distinct?: boolean
  /** `array_agg(x order by y)` : the order rows are fed to the aggregate. With `distinct`, every term must also be an argument. */
  order_by?: readonly (
    | Expression<K>
    | readonly ["asc" | "desc" | "asc-nulls-first" | "desc-nulls-last", Expression<K>]
  )[]
}

export type Expression<K extends string = string> =
  | null
  | true
  | false
  | number
  /** A bare string is always a literal string value — never a column/alias reference.
  To reference a column, use ["col", name] (or the read-only/write-only ["get", ...] / ["set", ...]). */
  | string
  | readonly [UnaryOperator, Expression<K>]
  | readonly [BinaryOperator, left: Expression<K>, right: Expression<K>]
  | readonly ["between", min: Expression<K>, exp: Expression<K>, max: Expression<K>]
  | readonly ["not_between", min: Expression<K>, exp: Expression<K>, max: Expression<K>]

  // Big integers as strings ; responses keep them exact only with a codec that does.
  | readonly ["bigint", value: string]
  | readonly ["numeric", value: string] // for really big numbers
  /** "."/"dot" : its operands are bare names, not literals. A leading bare name resolves against the current scope
  (a column, an alias, a joined field or an earlier computed key ; ["col", ...] only takes real columns) ; a leading
  sub-expression is used as-is. Each further operand is a name looked up on the previous result, so
  [".", "a", "b"] is [".", [".", "a"], "b"]. Only the leading name is type-checked. */
  | readonly ["." | "dot", name: K, ...hops: string[]]
  | readonly ["." | "dot", base: Expression<K>, hop: string, ...hops: string[]]
  | readonly [Exclude<FoldedOperator, "." | "dot">, ...Expression<K>[]]
  | readonly ["in" | "not_in", subject: Expression<K>, ...canditates: Expression<K>[]]
  | readonly [
      "any" | "all",
      op: FoldedOperator | BinaryOperator,
      subject: Expression<K>,
      array_or_list: Expression<K>,
    ]
  | readonly ["concat_ws", separator: Expression<K>, ...Expression<K>[]]
  | readonly ["coalesce", ...Expression<K>[]]
  | readonly ["format", format: string, ...Expression<K>[]]

  /** Aggregate an incoming relation's expression, callable only from the parent relation ; the optional last
  operand is an options object (`AggOptions`). `identifier` must be allowed, so it is a static name, not an
  Expression. For an overloaded aggregate, cast an untyped literal argument (`["::", value, "numeric"]`). */
  | readonly [
      "agg" | "aggregate",
      identifier: FunctionIdentifier,
      arguments: readonly Expression<K>[],
      options?: AggOptions<K>,
    ]
  /** A function call ; `identifier` must be allowed — same static-name constraint as "agg" above. */
  | readonly ["call", identifier: FunctionIdentifier, ...arguments: Expression<K>[]]

  // Expressions that produce objects
  /* an inline object that will become an object expression */
  | { [name: string]: Expression<K> }
  /** "*" = full (own columns + joined rels ; select's default value). "*~" = own (this relation's columns only).
  Trailing arguments, any number in any order : K[] lists columns to drop, an object adds computed keys (later keys
  win). A computed key may replace a dropped column, but shadowing a kept one is an error. */
  | readonly ["*" | "*~", ...(readonly K[] | { [name: string]: Expression<K> })[]]
  | readonly ["arr" | "array", ...Expression[]] // may need to be behind a flag ?
  | readonly ["index", array: Expression, index: Expression] // 1-indexed, just like PG
  | readonly ["slice", array: Expression, from: Expression, to: Expression] // 1-indexed, just like PG
  | readonly ["lst" | "list", ...Expression[]] // may need to be behind a flag ?

  // More granular field selection.
  // The default expression may be the "default" keyword if the column has a default value
  | readonly ["col", column: K, default_get?: Expression, default_set?: Expression] // this is to set default values instead of null in read or write
  | readonly ["get", column: K, default_value?: Expression] // this column will not be looked for / modified in write mode
  | readonly ["set", column: K, default_value?: Expression] // this column is not fetched in query mode, but is expected there in write mode.
  | readonly ["$param", name: string, cast?: string] // for use with well known queries
