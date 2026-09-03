/*!
This file specifies the shape of the JSON queries that rel is to understand.
*/

// A query to the server can be a single query or a sequence of queries.
// A query sequence is run in order in the same transaction ; an error in one of them fails the transaction.
// For more complex scenarii, it is recommended to use a function
export type Query = WriteQuery | RelationQuery | WellKnownQuery | Query[]

/* Call a query that's registered in rel. This can be seen as a view, except it's a rel's signature bi-directional query that's both readable and writable. */
export interface WellKnownQuery {
  wellknown: string
  params?: unknown
}

export interface WriteQuery {
  query: RelationQuery | WellKnownQuery

  /**
  The data to modify the database with. It must conform to the shape of the query.
  */
  data: unknown
}

export type Keys<T> = Extract<keyof T, string>

export interface RelationQuery<
  Rel extends object = { [name: string]: unknown },
  Join extends { [name: string]: RelationQuery } = { [name: string]: RelationQuery },
  FunctionArguments extends Expression<Keys<Rel>>[] | { [name: string]: Expression<Keys<Rel>> } =
    | Expression<Keys<Rel>>[]
    | { [name: string]: Expression<Keys<Rel>> },
> {
  // Identifying fields for the relation : exactly one of `relation` or
  // `function` must be given ; supplying both, or neither, is an error.
  /** The name of a table or view to query. Mutually exclusive with `function`. */
  relation?: string

  /**
    The name of a function to call, instead of querying a table/view directly.
    Mutually exclusive with `relation`.

    If the function is table-valued, joining can be performed just like if the query is about
    the type of the returned table. A function-rooted (or function-embedded) node is NEVER
    writable, unconditionally — regardless of whether its return type resolves to a real,
    otherwise-writable table — see `query-engine.md ## Reading Algorithm ### Function-rooted
    nodes` for the full reasoning (writes always target the underlying table directly, never
    "through" the function, so any filtering the function's own body does would otherwise be
    silently bypassed).
  */
  function?: string

  /** If not provided, the relation/function will be looked for in the search path. */
  schema?: string

  // give an alias to the relation in the query, usable by children and sibling relations' expressions
  // it is _not_ the same as the object keys in join ; those are accessible by the _parent_ relation to select
  alias?: string

  /**
   The columns to join on. The keys of the object refer to columns of the relation in the relation being described here, while the values refer to columns of the relation of the enclosing query. This field is mandatory on joined relations.

   Joining is limited to columns that are part of a foreign key constraint, or to distant columns where a unique constraint exists on either the local columns or the parent columns.

   The relation's OWN `on` columns (the child side being described here, whichever side of the resulting embed ends up "one" or "many") MUST additionally be covered by an index on those exact columns, or the query is rejected — see `query-engine.md` ### Join eligibility. This applies uniformly to FK-backed and non-FK joins alike : Postgres does not automatically index the referencing side of a foreign key, so an FK-backed embed is just as capable of silently compiling into a per-parent-row sequential scan as an ad-hoc one.
  */
  on?: { [local_column: string]: string }

  /**
    Positional or named arguments to pass to `function`. Only valid alongside
    `function` ; an error if given alongside `relation`. Absent (or an empty
    array/object) for a function that takes no arguments.
  */
  arguments?: FunctionArguments

  /** Restrict the rows produced by this relation according to a condition. */
  where?: Expression<Keys<Rel> | Keys<Join>>

  /**
    Write mode is only read when POSTing data and handles how the data of this particular relation in the query is to be handled.

    Delete-bearing modes (`merge`, `merge-new`, `merge-update`, `deleteonly`) only make sense on an
    _incoming_ relation : one whose rows are exclusively owned/scoped by the parent row through the join
    (this is what "rows not in the payload, matching the parent" even means) — commonly, but not
    necessarily, backed by a declared foreign key ; see `query-engine.md` `### Definitions` and
    `### Join eligibility`. An _outgoing_ relation (a to-one "belongs to", e.g. `user.manager_id ->
    manager.id`) is not exclusively owned by the current row - the referenced row may be pointed to by
    any number of other rows - so there is no coherent set of "rows not in the payload" to delete. Using
    a delete-bearing mode on an outgoing relation is a validation error, raised when the query is
    prepared.

    Defaults : `insert` for the outermost/root relation, `merge` for an incoming subquery, `upsert` for
    an outgoing subquery (never a delete-bearing mode, since that would be an error by the rule above).

    If you need delete-on-absence semantics for what looks like a to-one relationship (e.g. a `settings`
    row exclusively owned by a user but modeled with the FK on the `settings` side for nullability), model
    it as an incoming relation instead (put the FK, or the unique/indexed columns, on the other table)
    rather than trying to force it through an outgoing embed.
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
    /** Only update existing rows but ignore new ones and don't delete non-matching rows. */
    | "update"
    /** Delete only rows matching the where condition and not in data. Not valid on an outgoing relation (see above). */
    | "deleteonly"

  /** When provided,
    when a single string: the constraint name
    when an array : the columns
    with UNIQUE or PRIMARY KEY that will be used for conflict resolution when inserting/updating/merging.

    By default, on_conflict will be performed on the PRIMARY KEY only. While UNIQUE constraints _can_ be used, they are not used by default when there is no PRIMARY KEY. */
  on_conflict?: string | string[]

  /**
    When writing, limits which physical columns will be considered from the payload.
    If update_columns is not specified, also apply to updates.
    If unspecified, insert will be performed on all writable columns.
  */
  insert_columns?: string[]

  /** Limit the columns that are to be updated if a row already existed. If unspecified, all columns will be updated with provided values. */
  update_columns?: string[]

  /**
   Add a related table to the query. The keys of the join object is the alias that can then be used in the select expression.

    As Rel is about relationship between rows of data and not about joining rows of data arbitrarily, the result of joins that would fetch several rows will be an array and allow for aggregation operations.

   Otherwise, the result will be an object that can be deconstructed using the "." operator.

  */
  join?: Join

  /**
   The shape of what will be returned by the select.
   I not specified, then it will be equivalent to select * from the relation, as well as the embeds defined by join if any.

   An error is raised when there is no select clause and a join alias conflicts with a column name.

   Doesn't have to be shape-producing (own/full/their variants, an object literal, or a bare
   get/get-set) — any other expression is valid too, and produces one bare JSON value per row
   instead of a one-key object : a flat array of scalars at the root or a to-many embed, or a
   single bare value for a to-one embed — see `query-engine.md ## Reading Algorithm ###
   Scalar-selected nodes`.
  */
  select?: Expression<Keys<Rel> | Keys<Join>>

  distinct?: boolean

  distinct_on?: Expression[]

  // if not supplied, "asc" is the default, just like in SQL
  order_by?: (
    | Expression
    // asc and desc are nulls last by default
    | ["asc" | "desc" | "asc-nulls-first" | "desc-nulls-last", Expression]
  )[]

  // The following two clauses are SQL's clauses. When used in a subquery, applies them for each parent-row
  offset?: number
  limit?: number
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

// These operators are binary operators but that can be applied over a long list starting from the left and two by two
// ["-", 4, 3, 2, 1] -> ["-", ["-", ["-", 4, 3], 2], 1]
// Boolean operators are treated as and
// ["<", 1, 2, 3, 4] -> ["and", ["<", 1, 2], ["<", 2, 3], ["<", 3, 4]]
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

// Here are all binary for who folding makes little sense
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

/** Names a function/aggregate for "call"/"agg" — a bare string (an
unqualified name, resolved against the configured search path) or an
explicit { schema, name } object. Never a single "schema.name" string : a
schema-qualified Postgres identifier can itself contain a literal dot when
quoted (`"my.schema".func`), so splitting a combined string apart would need
a real quote-aware identifier parser rather than a plain string split. A bare
string here is never split looking for a schema — it's always the whole,
unqualified name. */
export type FunctionIdentifier = string | { schema: string; name: string }

export type Expression<K extends string = string> =
  | null
  | true
  | false
  | number
  | "*" // select all fields of the current relation + aliases
  /** strings always refer to aliases and column names, since they are much more likely to appear than actual strings */
  | K
  /** a string literal is an array of only one string. Exception : the
  strings "own" and "full" always dispatch to the ["own"]/["full"] forms
  below instead — those are the only two one-element-string-array tags
  that take zero further arguments, so the literal string values "own" and
  "full" cannot be produced this way ; there is currently no way to express
  them as a string-literal Expression at all. */
  | [string]
  | [UnaryOperator, Expression<K>]
  | [BinaryOperator, left: Expression<K>, right: Expression<K>]
  | ["between", min: Expression<K>, exp: Expression<K>, max: Expression<K>]
  | ["not_between", min: Expression<K>, exp: Expression<K>, max: Expression<K>]

  // explicit bigint support for queries. in responses, the user can choose to have another parser than JSON.parse _if_ they absolutely need bigints
  | ["bigint", value: string]
  | ["numeric", value: string] // for really big numbers
  | [FoldedOperator, ...Expression<K>[]]

  // avoid having to create ["arr", ...] for the contained expression
  // here is an exception : candidates literal strings are here treated as literal strings and not columns. Column comparison should be performed by other operators
  | ["in" | "not_in", subject: Expression<K>, ...canditates: (string | Expression<K>)[]]
  | [
      "any" | "all",
      op: FoldedOperator | BinaryOperator,
      subject: Expression<K>,
      array_or_list: Expression<K>,
    ]
  | ["concat_ws", separator: Expression<K>, ...Expression<K>[]]
  | ["coalesce", ...Expression<K>[]]
  | ["format", format: string, ...Expression<K>[]]

  /** Aggregate an expression. `identifier` must be an allowed aggregate function. The second expression is the expression to aggregate. It must be an incoming relation. The last expression, if given, is a filter expression. Aggregates can only be called from a parent relation.

  `identifier` is a FunctionIdentifier, not an Expression : function/operator allowlisting (see query-engine.md ## Scoping) has to be checkable at query-compile time against a static, schema-qualified name, which isn't possible if the identifier could itself be a computed expression. */
  | [
      "agg" | "aggregate",
      identifier: FunctionIdentifier,
      arguments: Expression<K>[],
      filter?: Expression<K>,
    ]
  /** A function call. `identifier` must be an allowed function — see the note on "agg" above ; the same constraint applies here. */
  | ["call", identifier: FunctionIdentifier, ...arguments: Expression<K>[]]

  // Expressions that produce objects
  /* an inline object that will become an object expression */
  | { [name: string]: Expression<K> }

  // Neither own nor full add computed columns by default ; yet, they're selectable
  // there are functions that take the table's type as first argument and reply a result that can thus be integrated this way
  // these columns can NEVER be written to.
  | ["own"] // an object with all the columns of the current relation ; takes precedence over the [string] literal form above
  | ["full"] // a variant ; includes the joined rels. This is select's "default" value ; also takes precedence over [string]
  /* Similar, but omits columns */
  | ["own_except" | "full_except", except: K[]]
  /* Similar, but adds computed columns */
  | ["own_and" | "full_and", and: { [name: string]: Expression<K> }]
  /* Select all except omitted_keys and add the computed keys in merge_with. merge_with can specify keys that were omitted ; they shall override it. merge_with cannot shadow keys implicitely ; this is an error */
  | ["own_except_and" | "full_except_and", except: K[], and: { [name: string]: Expression<K> }]
  | ["arr" | "array", ...Expression[]] // may need to be behind a flag ?
  | ["index", array: Expression, index: Expression] // 1-indexed, just like PG
  | ["slice", array: Expression, from: Expression, to: Expression] // 1-indexed, just like PG
  | ["lst" | "list", ...Expression[]] // may need to be behind a flag ?

  // More granular field selection.
  // The default expression may be the "default" keyword if the column has a default value
  | ["get-set", column: K, default_get?: Expression, default_set?: Expression] // this is to set default values instead of null in read or write
  | ["get", column: K, default_value?: Expression] // this column will not be looked for / modified in write mode
  | ["set", column: K, default_value?: Expression] // this column is not fetched in query mode, but is expected there in write mode.
  | ["$param", name: string, cast?: string] // for use with well known queries
