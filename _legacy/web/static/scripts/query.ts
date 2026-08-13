export type SelectExpression =
  | Expression
  | {
      [name: string]: SelectExpression
    }

export interface FullQuery {
  query: Query

  /**
    The data to modify the database with. If empty, the query is just a selection.
    If provided, it must conform to the shape of the select expression.
  */
  data: unknown[]
}

export type ServerQuery = Query | FullQuery | (Query | FullQuery)[]

export interface RelationName {
  relation: string
  schema?: string
}

export interface Write {
  /** The relation to write to. If not provided, it will be the same as the relation of the query. */
  relation?: RelationName

  mode?: /** Default mode. Only select the data. If data is provided, it will be ignored. */
    | "readonly"
    /** Insert new rows and do not touch the already exiting ones. */
    | "insert"
    /** Insert or update (... on conflict do update), but do not delete non-matching rows. */
    | "upsert"
    /** Delete rows not in the payload that match the where condition as well as foreign key relationships with a parent row if this query is a subquery, insert new ones and update existing ones. */
    | "merge"
    /** Delete and insert but do not update. */
    | "mergenew"
    /** Delete and update, but do not insert. */
    | "merge-update"
    /** Only update existing rows but ignore new ones and don't delete non-matching rows. */
    | "update"
    /** Delete only rows matching the where condition and not in data. */
    | "deleteonly"

  on_conflict?: string[]
  insert_columns?: string[]
  update_columns?: string[]
}

export interface Query {
  // Identifying fields for the relation :
  /** The relation name, or the function name in the case of functions */
  relation: RelationName

  /** The write operation to perform on the relation. */
  write?: Write

  /** If arguments is provided, then call a table-valued function. Links are usable from table-valued functions, and modifying statements will apply to the underlying relation if it is a table. However, all merge/delete operations are disallowed. */
  arguments?: Expression[] | { [name: string]: Expression }

  /** Restrict the rows produced by this relation according to a condition. */
  where?: Expression

  /** When provided, the constraint name or columns with UNIQUE or PRIMARY KEY that will be used for conflict resolution when inserting. By default, on_conflict will be performed on PRIMARY KEY or a UNIQUE constraint if there is only one defined on the table. */
  on_conflict?: string[]

  /** When writing, limits which physical columns will be considered from the payload. If update_column is not specified, also apply to updates. If unspecified, insert will be performed on all columns with their default values. */
  insert_columns?: string[]

  /** Limit the columns that are to be updated if a row already existed. If unspecified, all columns will be updated with provided values. */
  update_columns?: string[]

  /**
   Add a related table to the query. The keys of the join object is the alias that can then be used in the select expression.

    As Rel is about relationship between rows of data and not about joining rows of data arbitrarily, the result of joins that would fetch several rows will be an array and allow for aggregation operations.

   Otherwise, the result will be an object that can be deconstructed using the "." operator.

  */
  rels?: {
    [alias: string]: Query
  }

  /**
   The fields to select from the relation and its joins.

   By default, all fields are selected and joined relations are included as their aliases.
  */
  select?: SelectExpression

  /** Similar to SQL's OFFSET clause */
  offset?: number

  /** Similar to SQL's LIMIT clause */
  limit?: number
}

export type UnaryOperator = "-" | "not" | "~"

export type BinaryOperator =
  | "="
  | "<>"
  | ">"
  | ">="
  | "<"
  | "<="
  | "distinct"
  | "not distinct"
  | "and"
  | "or"
  | "in"
  | "not in"
  | "is"
  | "is not"
  // | "any"
  // | "all"
  | "like" // warning : need configuration as they can be abused for DDoS attacks
  | "ilike" // warning : need configuration as they can be abused for DDoS attacks
  | "~" // warning : need configuration as they can be abused for DDoS attacks
  | "~*" // warning : need configuration as they can be abused for DDoS attacks
  | "::" // type cast
  | "+"
  | "-"
  | "*"
  | "/"
  | "%"
  | "&&"
  | "<<"
  | ">>"
  | "&"
  | "|"
  | "^"
  | "."
  | "->"
  | "->>"
  | "??"
  | "?|"
  | "?&"
  | "?:"

export type Expression<K extends string = string> =
  | null
  | number
  /** strings always refer to aliases and column names, since they are much more likely to appear than actual strings */
  | "*"
  | string
  | boolean
  | { [name: string]: Expression }
  | [UnaryOperator, Expression]
  | [BinaryOperator, left: Expression, right: Expression]
  | ["between", min: Expression, exp: Expression, max: Expression]
  | ["not between", min: Expression, exp: Expression, max: Expression]
  /** Concatenate. Coalesces all operands and makes sure there always is a string result. */
  | ["concat", ...Expression[]]
  | ["concat_ws", separator: Expression, ...Expression[]]
  | ["coalesce", ...Expression[]]
  | ["format", format: string, ...Expression[]]

  /** Aggregate an expression. The first expression must resolve to an allowed aggregate function. The second expression is the expression to aggregate. It must be an incoming relation. The last expression, if given, is a filter expression. Aggregates can only be called from a parent relation. */
  | ["agg" | "aggregate", identifier: Expression, arguments: Expression[], filter?: Expression]
  | ["call", identifier: Expression, ...arguments: Expression[]]
  | ["*", ...exclude: string[]] // the star operator. Only applies to the table of the current query.

  /** A function call. Expression must resolve to an allowed function */
  | ["arr" | "array", ...Expression[]] // may need to be behind a flag ?
  | ["lst" | "list", ...Expression[]] // may need to be behind a flag ?

  // Field selection. col allows writing, while get only allows reading and set only writing - the associated column will not appear in the result.
  // The default expression may be the "default" keyword if the column has a default value
  | ["col", column: K, default_get?: Expression, default_set?: Expression]
  | ["get", column: K, default_value?: Expression]
  | ["set", column: K, default_value?: Expression]
  | [column: string]

/**
  Example :

{
  name: "movie",
  schema: "api",
  where: [
    [">=", "year", 1999]
  ],
  join: {
    actor: {
      name: "actor",
      schema: "api",
      on: {"actor_id": "movie_id"},
    },
  }
  select: {
    movie: ["omit", "movie_id", "year"],
    actors: "actor",
  }
}
*/
