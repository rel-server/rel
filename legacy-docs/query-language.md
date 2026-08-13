# The `query` Package: A Postgres-Native Query DSL

Legacy source: `/home/chris/Code/rel/_legacy/query/` (Go module `sales-way.com/server`).

## 1. Purpose

`query` is a hand-written lexer/parser/compiler that turns a compact, PostgREST-like
textual query string into a **single SQL statement** that Postgres executes to
produce a nested JSON document directly (via `row_to_json`/`json_agg` and
correlated subqueries — see §4). A client describes, in one string:

- which table/function to hit and which verb to apply (get/insert/update/upsert/
  delete/merge),
- which columns and related tables ("relationships") to embed, arbitrarily nested,
- filters (`where`), ordering, limit/offset,
- and (for write verbs) that the JSON request body should be flattened and
  written recursively across the relationship graph in one transaction.

It intentionally rides on Postgres's own semantics: table/column identifiers,
operators, casts (`::`), JSON operators (`->`, `->>`), and role-based access all
mirror Postgres directly rather than reinventing an abstraction layer. Security
(row visibility, column grants) is not modeled by this package at all — see §7.

The parser is a classic **Pratt (precedence-climbing) parser** hand-rolled on top
of a custom lexer; there is no parser generator. `query/parse2_pratt.go:94-140`
mirrors Postgres's actual operator precedence table (comment block at
`query/parse2_pratt.go:5-24`, transcribed from the Postgres docs) so that
expressions embedded in the query string parse with the same precedence a
Postgres `WHERE` clause would.

`query/readme.md` documents an *informal, partly aspirational* grammar that does
not fully match the implementation (e.g. it shows `@relation` and
`(select_clause)` for embedding; the real parser uses `<<`/`>>` and `{ }` — see
§2/§9). Treat the readme as a design sketch, not ground truth; this document
describes the parser as implemented.

## 2. Syntax, reconstructed from the parser

### 2.1 Top level

```
toplevel   := operation (";" operation)*
operation  := "rollback"? verb? table_ref selection
verb       := "insert" | "update" | "delete" | "upsert" | "merge"   (case-insensitive)
table_ref  := [alias ":"] dotted_identifier      -- e.g. api.users, or "schema"."My Table"
```
(`query/parse2.go:53-129`, `ParseTopLevelOperation`.) If no verb keyword is
present, the operation defaults to a **GET** (`AstOperationType` zero value,
`query/ast.go:11`, and `ParsingContext.DefaultVerb` in `query/context.go:8-13`).
`rollback` before the verb marks the operation to run inside a `SAVEPOINT` that
is rolled back after execution — a dry-run mechanism (`query/ast.go:23`,
wired up in `pg2.go:249-253,305-307`).

Multiple operations can be chained with `;` in one request string
(`query/parse2.go:22-49`); the HTTP layer wraps their outputs into a single
JSON array (`pg2.go:239-241,309-315`).

`insert` / `upsert` / `merge` additionally require a **data operand expression**
followed by the keyword `into` before the table name:
```
insert <expr> into api.users ( ... )
```
(`query/parse2.go:78-89`). In practice the operand is almost always the literal
symbol `$body` (bound to the JSON request payload — see `AddBody`,
`query/sql_resolve.go:41-43`, which rewrites `$body` to `($1::jsonb)`).
Curiously, **plain `update` does not go through this operand/`into` branch**
(only INSERT/UPSERT/MERGE do) — see the limitations in §8.

### 2.2 Selection (the part in `( ... )` / `{ ... }` / clauses after the name)

```
selection      := function_args? where_clause? field_block? order_clause? limit_clause? offset_clause?
function_args  := "(" [ (name ":")? expr ] ("," (name ":")? expr)* ")"
where_clause   := "where" expr
field_block    := "{" field ("," field)* "}"
order_clause   := "order" "by"? order_item ("," order_item)*
order_item     := expr ("asc" | "desc")?
limit_clause   := "limit" NUMBER
offset_clause  := "offset" NUMBER
```
(`query/parse2.go:131-214`.) `function_args` doubles as PostgreSQL function-call
argument syntax when the target `table_ref` actually resolves to a set-returning
function (see §6/`sql_resolve.go:75-108`) — arguments can be positional or
named (`name => value` on the SQL side, `name: value` in the DSL,
`query/parse2.go:216-257`).

### 2.3 Field list

```
field       := "*" ("-" ident)*                          -- star with exclusions
             | ident ":" "{" field ("," field)* "}"       -- named field group (nested JSON object)
             | ident ":" expr                             -- aliased scalar/expression field
             | ident                                       -- bare column (shorthand for ident: ident)
             | "readonly"? ("<<" | ">>") relation_ref selection
             | ident ":" "readonly"? ("<<" | ">>") relation_ref selection   -- aliased relationship
relation_ref := dotted_identifier ("(" ident ("," ident)* ")")?   -- optional FK/column-name hint
```
(`query/parse2.go:260-392`.)

- `*` selects every column of the current table; `* - col1 - col2` excludes
  columns (`ParseStarSelector`, `query/parse2.go:407-427`).
- `>>` embeds an **outgoing** relationship — a foreign key on *this* table
  pointing at another table (many-to-one / one-to-one; renders as a scalar
  object). `<<` embeds an **incoming** relationship — another table's foreign
  key pointing back at this one (one-to-many, unless the FK columns are
  themselves unique, in which case it's one-to-one). This mapping comes from
  `pg/relationships.go:107-137` (outside this package — see §3).
- The optional `(col1, col2)` after the relation name disambiguates which
  foreign key/constraint to use when several relationships connect the same
  two tables by name (`query/parse2.go:341-357`); it is resolved against
  `DBTable.RelationshipsMap` keyed by strings like `>>other_table(col1,col2)`.
- `readonly` marks an embedded relation as read-only for write verbs: its rows
  are still selected but never targeted by insert/update/delete cascading
  (`query/parse2.go:324-327`, consumed in `query/json_data.go:112-116`).
- There's a vestigial `[` `]` check right after the relationship hint
  (`query/parse2.go:360-366`) that only succeeds if the brackets are empty and
  otherwise silently rewinds — dead/reserved syntax, not documented anywhere,
  seemingly a leftover from a discarded "array of selections" idea.
- A named field group `alias: { ... }` produces a **nested JSON object that is
  not a relationship** — it's implemented purely as a SQL scalar subquery
  wrapper (`(SELECT Q FROM (SELECT ...) Q) AS "alias"`,
  `query/sql_constructs.go:111-124`), useful for grouping plain columns/
  expressions under a sub-key without a join.

### 2.4 Expressions

```
expr := ident
      | number | string
      | unary_op expr
      | expr binary_op expr
      | expr "(" arg_list ")"                 -- function call, or field access via computed-column fn
      | "(" expr ("," expr)* ")"               -- parenthesized expr, or an expression *list* if >1 items
      | "[" expr ("," expr)* "]"               -- ARRAY[...]
      | expr "::" dotted_identifier            -- Postgres-style cast
      | expr "." ident                          -- name resolution / JSON field access chain
```
(`query/parse2_pratt.go:41-89,142-171`.) `Identifier`, `AstBinOp`, `AstPrefixOp`,
`AstFunctionCall`, `AstArray`, `AstExpressionList`, `AstRawSql` are the AST node
types (`query/ast.go:156-205`). Numbers and strings are carried through
essentially verbatim as `AstRawSql` (`query/parse2_pratt.go:84-85`) — no literal
type-checking happens in this layer; Postgres does that at execution time.

### 2.5 A realistic, syntactically-supported example

Note the clause order matters and is easy to get wrong by analogy with SQL:
inside `ParseSelection` it is `(function-args)` → `where ...` → `{ field-block
}` → `order by ...` → `limit ...` → `offset ...`
(`query/parse2.go:131-214`) — `where` comes **before** the `{ }` field block,
not after it.

```
mine: api.users
  where department = 'sales' and active is not null
  {
    * - password_hash,
    manager: >> manager,
    reports: << users(manager_id) { id, name },
    readonly << roles_users { role: >> role { id, name } }
  }
  order by created_at desc
  limit 20 offset 40
```

(`mine:` is an optional alias for the whole operation, consumed as
`IDENT ":"` *before* the dotted table name, `query/parse2.go:91-100`. The
`>>`/`<<` target must name a distant *table or constraint* —
`RelationshipsMap` keys are built from the distant table's name/db-name or the
constraint name, optionally suffixed with a `(col,col)` hint
(`pg/relationships.go:121-135`) — never a bare column name like
`>> manager_id`.)

The `readme.md` example below is **readme-only and does not parse** against
the shipped grammar — it predates (or sketches, never-implemented) a `@`-based
relation syntax and parenthesized field list, neither of which the real parser
accepts (real syntax uses `<<`/`>>` and `{ }`, §2.3):
```
api.bilan ( $: *, @manager )
```

## 3. Relationships / joins

This package **does not discover relationships itself**. It consumes a
pre-built graph handed in from the `pg` package: `pg.DBAllTables`,
`pg.DBTable.RelationshipsMap`, and `pg.RelationShip`
(`query/parse2.go:14`, `query/context.go:8-13`, `query/sql_resolve.go:178`).
`pg/relationships.go:29-105` walks Postgres foreign-key constraints once at
startup and, for every FK, registers **two** `RelationShip` records: a forward
one (`IsReverse:false`, operator `>>`) on the table holding the FK, and a
reverse one (`IsReverse:true`, operator `<<`, `IsMultiple` set unless the FK
columns are unique on the far side) on the referenced table
(`pg/relationships.go:67-95,107-137`). Ambiguous name collisions are recorded as
a `nil` map entry (`pg/relationships.go:157-160`) and surfaced later as
`"relationship %s is ambiguous"` (`query/sql_resolve.go:182`).

Inside `query`, resolution happens in `AstSelection.resolveField`
(`query/sql_resolve.go:161-206`): it builds the lookup key
`operator + relationship_name (+ "(col,col)")`, looks it up in
`RelationshipsMap`, then recursively resolves the embedded selection against
the distant table. Resolved relationships are bucketed into
`ResolvedOutgoingRelationships` / `ResolvedIncomingRelationships` on
`AstSelection` (`query/ast.go:40-41`) — this split is what later decides scalar
object vs. array aggregation in SQL generation.

Aliasing: every relationship field carries its own `Alias` (defaulting to the
identifier used, or the explicit `name:` given) which becomes both the SQL
subquery's `AS "alias"` and the resulting JSON key (`AstRelationshipField.
JsonName`, `query/query_helpers.go:3-13`, appends `[]` to the key when
`IsMultiple`).

## 4. AST → SQL compilation and the JSON shape

Confirmed hypothesis: this is a compiler from the DSL AST straight to Postgres
SQL that returns **one column containing a JSON value**, built through nested
correlated scalar subqueries rather than explicit `LATERAL` joins or a single
flat `json_agg` over a join.

Core routine: `AstSelection.sqlSelectQuery` (`query/sql_constructs.go:172-250`):

```sql
SELECT coalesce(json_agg(_R), '[]'::json) as res FROM (
  SELECT <field1>, <field2>, ... , <relationship-subqueries-as-columns>
  FROM "schema"."table" "alias"
  WHERE (...)
  ORDER BY ...
  LIMIT n OFFSET m
) _R
```
or, for a single-row (to-one / top-level scalar) result:
```sql
SELECT coalesce(row_to_json(_R), 'null'::json) as res FROM ( SELECT ... ) _R
```
The choice between `json_agg` (array) and `row_to_json` (object) is made by
`returns_set` (`query/sql_constructs.go:187`): true when the target is a
set-returning function, or (for tables) when there is no enclosing relationship
or the enclosing relationship is `IsMultiple`.

Each **embedded relationship** compiles to a parenthesized, fully self-contained
`sqlSelectQuery` placed directly as one item in the outer `SELECT` list,
correlated to the outer row by injecting an equality predicate into the
sub-selection's own `WHERE` clause before recursing
(`AstRelationshipField.sqlSelect`, `query/sql_constructs.go:50-88`):
```go
"alias_outer"."local_fk" = "alias_inner"."distant_col"   -- ANDed into inner WHERE
```
So the generated SQL looks conceptually like:
```sql
SELECT "id", "name",
  (SELECT coalesce(row_to_json(_R),'null'::json) FROM (
      SELECT "id","name" FROM "public"."manager" "manager1"
      WHERE ("manager1"."id" = "users"."manager_id")
   ) _R) AS "manager"
FROM "public"."users" "users"
```
This is a correlated-subquery-in-SELECT-list pattern (Postgres allows the inner
query to reference the outer row's alias without an explicit `LATERAL`
keyword because it's a plain scalar/array subquery evaluated per outer row) —
functionally similar to what a `LATERAL` join + `json_agg` would produce, but
implemented as N nested subqueries rather than N joined relations. There is no
single flattened join; depth of embedding directly equals subquery nesting
depth. Named field groups (`alias: { ... }`, not a relationship) use the same
pattern minus the correlation predicate (`AstFieldGroup.sqlSelect`,
`query/sql_constructs.go:111-124`).

**Non-GET verbs** are not compiled by `query` itself but by the sibling
`templates/` package (`templates/json_flat.go`, `templates/json_table.go`,
generated via `go:generate qtc` from `.pat` sources, consuming `*query.
AstSelection`). The write path:
1. The client's JSON body is flattened depth-first into `(node_id, parent_id,
   selection_index, json)` tuples that mirror the relationship tree
   (`query/json_data.go`, `AstOperation.FlattenJsonData`).
2. Those tuples are `COPY`'d into a `CREATE TEMP TABLE __flat_json_temp (...)
   ON COMMIT DROP` (`pg2.go:280-283`).
3. `templates` emits a chain of CTEs — `INSERT ... RETURNING *`,
   `UPDATE ... RETURNING *`, or `DELETE ... RETURNING *` per table in the
   graph, walking outgoing relationships before incoming ones so foreign keys
   are satisfied in order (`templates/json_flat.go:16-100+`) — materialized
   into a temp table via `CREATE TEMP TABLE <temp_dml_name> ON COMMIT DROP AS
   WITH ... SELECT * FROM ...` (`templates/json_table.go:16-33`).
4. `query`'s own `sqlSelectQuery` then runs a normal SELECT, but sources rows
   from that temp DML table instead of the base table when the selection is a
   top-level, non-GET operation (`query/sql_constructs.go:180-185`,
   `sel.TempDMLTableName()`), so the same JSON-shaping machinery produces the
   response for writes as for reads.

## 5. Filter/expression operator support

Tokenization of operators: `query/lexer.go:296-363` (generic Postgres-style
operator character scan, including the "no dash/plus at wonky end" rule that
mirrors Postgres's actual operator-naming restriction, `scanOperator`,
`query/lexer.go:379-437`) plus a fixed set of *keyword* operators recognized
case-insensitively: `not, and, or, is, isnull, notnull, between, in, like,
ilike, similar, similarto` (`query/lexer.go:334-349`).

Precedence table (`query/parse2_pratt.go:94-140`), lowest to highest: `or` <
`and` < `not` < `is/isnull/notnull` < comparisons (`< > = != <= >=`) <
`between/in/like/ilike/similar/similarto` < *(any other/unrecognized
operator)* < `+ -` < `* / %` < `^` < `at` < `collate`/`(` (function call) <
`[` (subscript) < `::` (cast) < `.` (field/dotted access, highest).

**Binary operators actually wired into the parser's infix handler**
(`Xp2Led`, `query/parse2_pratt.go:142-171`) — i.e. what will really parse —
cover typecast `::`, dotted access `.`, arithmetic `+ - * / % ^`, Postgres
JSON/array/bitwise operators `& | # << >> -> ->> #> #>> ? ?| ?& @> <@`,
comparisons `= != <> < <= > >=`, string concat `||`, regex `~ ~* !~ !~*`,
geometric operators (`@ ~= &< &> <<| |>> |&< |&>`), and the keywords
`and`, `or`, `is`.

Unary/prefix operators (`Xp2Nud`, `query/parse2_pratt.go:42-57`): `not`, `-`,
`+`, `~`.

## 6. Consumers / how a query is triggered

`query.go` itself is essentially empty (`package query` plus a
`//go:generate qtc -dir=templates` directive, `query/query.go:1-2`) — the
package's public surface is spread across `Parse2`, `NewResolveContext`/
`ResolveContext.AddBody`, `NewContext`/`NewRootScope`, `AstOperations.Resolve`,
and `AstOperation.SqlSelect`/`FlattenJsonData`.

The single HTTP entry point is `SetupPG2Routes` in `pg2.go:195-322`, mounted at
`/rel` and `/rel/*` (`pg2.go:321-322`), wired from `SetupPG` (`pg.go:8-19`, run
behind the JWT auth middleware and gzip). Any HTTP method works since
`router.Handle` is method-agnostic. The query string itself comes from, in
priority order: the `X-Query` request header, the `query` form field (for
multipart requests), or the literal URL path/tail after `/rel/`
(`getTinySQLRequest`, `pg2.go:142-175`) — so a GET with the DSL string in the
URL, or a POST with it in a header, both work; the JSON write payload (for
insert/update/upsert/merge) is the actual HTTP request body (or a `payload`
multipart field). No use of this package was found in `websocket.go` /
`websocket-session.go` — the websocket protocol is a separate mechanism
unrelated to this query language.

Sequence per request (`pg2.go:197-320`): `query.Parse2` → build a child scope
off `srv.RootScope` (itself built once at startup by `query.NewRootScope`,
`sw/defs.go:111`, exposing tables/functions/schemas as symbols) →
`ops.Resolve` (binds names to real tables/columns/relationships,
`query/sql_resolve.go`) → for each op, optionally emit DML via `templates.
GenerateFlatQuery`/`GenerateDeleteQuery`, optionally flatten+COPY the JSON body
→ always emit the final SELECT via `op.SqlSelect` → hand the resulting list of
SQL statements to `runJsonArraySql`, which runs them all in one Postgres
transaction after `SET LOCAL ROLE "<jwt role>"` (`pg2.go:34-135`, role from
`jwtGetRoleFromRequest`).

## 7. Scoping / security model

`query` has **no concept of allowed tables, columns, or roles of its own**.
`Scope`/`NewRootScope` (`query/scope.go`) only performs *name resolution* —
mapping identifiers the client typed to real, schema-qualified SQL — restricted
to whatever schemas were passed to `NewRootScope(tables, functions, schemas)`
at startup (`query/scope.go:206-247`, `sw/defs.go:111`). Anything not present in
those schemas' table/function maps simply cannot be named, but that's a
visibility filter, not an authorization system.

All actual authorization is delegated to **Postgres itself via
`SET LOCAL ROLE`**: `runJsonArraySql` extracts a role string from the caller's
JWT (`jwtGetRoleFromRequest`) and executes `SET LOCAL ROLE "<role>"; SET LOCAL
"app.current_role" = '<role>'` before running any generated SQL
(`pg2.go:43,62-65`). Postgres RLS policies and column/table `GRANT`s (defined
outside this package, presumably in DB migrations not reviewed here) are what
actually gate access; a Postgres permission-denied error (`SQLSTATE 42501`) is
specifically caught and turned into HTTP 401/403 (`pg2.go:112-123`). In other
words: this package will happily *generate* SQL against any schema it was
initialized with, and correctness of access control lives entirely in the
database role/RLS layer, not in the query language.

## 8. Known limitations / incomplete features

- **`in` / `like` / `ilike` / `between` / `similar` / `similarto` /
  `isnull` / `notnull` are tokenized and given real operator precedence but
  are *not* implemented in the infix parser.** The lexer treats them as
  operator keywords (`query/lexer.go:334-349`) and `Xp2Lbp` assigns them
  precedence (`query/parse2_pratt.go:108-112`), but `Xp2Led`'s switch
  statement — the code that actually builds an `AstBinOp` for an infix
  operator — does **not** list any of them (`query/parse2_pratt.go:158`).
  A commented-out, larger case list at `query/parse2_pratt.go:156` shows
  they were once (or were meant to be) handled; as shipped, writing
  `where x in (1,2)` or `where name like 'a%'` in the query string hits the
  `default: return nil, tok.ErrorMessage("unknown operator")` branch
  (`query/parse2_pratt.go:170`) and fails to parse. `is`/`and`/`or` do work.
  (Note `x is not null` still parses correctly by accident, since `not` is a
  valid unary prefix operator that composes with `is`.)
- **`update` is parsed as a verb but never produces an UPDATE statement.**
  `ParseTopLevelOperation` only parses a data operand + `into` for
  `OP_MERGE`/`OP_UPSERT`/`OP_INSERT` (`query/parse2.go:79-89`); more tellingly,
  the HTTP handler's verb dispatch switch only has cases for `OP_MERGE`,
  `OP_INSERT`, `OP_UPSERT`, `OP_DELETE` (`pg2.go:255-268`) — there is no
  `case query.OP_UPDATE`, so an `update` operation silently falls through with
  an empty DML string and only runs the trailing SELECT, performing no
  mutation at all.
- The `[` `]` empty-bracket allowance right after a relationship's
  `(field_hint)` list (`query/parse2.go:360-366`) parses but does nothing —
  looks like a stubbed/abandoned syntax extension.
- `readme.md`'s grammar (`@relation`, parenthesized `select_clause`) does not
  match the shipped grammar (`<<`/`>>`, brace-delimited field blocks) — see
  §9; likely predates a syntax revision. It also documents verb keywords
  `get` and `call` that don't exist in code: `VERBS` is only `insert, update,
  delete, upsert, merge` (`query/parse2.go:12`) and GET is simply the
  *absence* of a verb keyword. Writing `get api.users` fails to parse (`get`
  is consumed as the table-name expression, then `api.users` is an
  unexpected trailing token).
- Numeric/string literals are passed through as raw, unvalidated SQL text
  (`AstRawSql`, `query/parse2_pratt.go:84-85`); type errors surface only when
  Postgres executes the statement.
- `printType`/reflection-based debug dumper left in production code
  (`query/sql_resolve.go:348-390`), called on an unresolvable simple-field
  expression right before returning an error (`query/sql_resolve.go:253`) —
  a debugging aid, harmless but not removed.
- `query/json_data.go:112-116` has a `FIXME` acknowledging that read-only
  relationships probably should still expose their foreign-key value even
  though writes to them are skipped; not implemented.
- Ambiguous relationship names raise a runtime resolve error asking the
  caller to qualify columns or constraint name (`query/sql_resolve.go:182`);
  there's no compile-time listing of what's ambiguous exposed to clients
  beyond that message.

## 9. Notes for the rewrite — relationship to sibling package `rel/`

This is the single most important finding for planning the new
`github.com/ceymard/rel` project: **the legacy tree already contains a
package literally named `rel/`** (`/home/chris/Code/rel/_legacy/rel/`,
package `sales-way.com/server/rel`), and it is clearly the *in-progress
successor* to `query/`, not an unrelated package. Evidence:

- `rel/*.go` **imports and directly reuses `query`'s expression/AST/scope
  machinery** — `query.IAstExpression`, `query.Identifier`, `query.AstBinOp`,
  `query.AstFunctionCall`, `query.AstRawSql`, `query.AstExpressionList`,
  `query.AstNamedFunctionArgument`, `query.Scope`, `query.Token`,
  `query.NewRewrittenName` (`rel/query.go:53-56`, `rel/from_json_expression.go`,
  `rel/from_json_select.go`, `rel/resolve_context.go:11-14`,
  `rel/query_resolve.go`, `rel/from_json.go:246-276`, `rel/parse_test.go:89`).
  It does **not** reuse `query`'s text lexer/Pratt parser (`parse2*.go`) —
  `rel` has its own `from_json*.go` front end that builds the same expression
  AST **from a parsed JSON body** (`ast.Node` from `sonic`) instead of from a
  DSL string. So the rewrite direction visible in the legacy code is: *keep
  the expression AST + scope/name-resolution core, replace the surface syntax
  with a JSON-shaped query instead of a bespoke string DSL.*
- The main server wires up **both** in parallel, as clearly labeled,
  differently-versioned routes: `pg2.go` (`query` package) serves `/rel` and
  `/rel/*`; `pg3.go` (`rel` package) serves `/rel2`
  (`pg3.go:14-73`, `pg.go:8-19` calls both `SetupPG2Routes` and
  `setupPg3Routes`). The `/rel2` naming and the fact that its handler
  (`pg3.go:16-70`) only parses, resolves, and reports diagnostics
  (`relation`, `readonly`, `data_count`, `flat_rows`) — **it never calls
  anything like `SqlSelect` or executes SQL** — strongly indicates `rel` was
  mid-development/experimental at the point this snapshot was taken: parsing
  and resolution exist, SQL generation/execution for the new front end does
  not appear wired up yet.
- Given the *new* project's module is `github.com/ceymard/rel`, this is
  almost certainly meant to continue from `_legacy/rel/`'s direction (JSON-
  based query shape reusing the mature expression/relationship-resolution
  core), not from `_legacy/query/`'s string DSL. Recommendation: when reading
  the (separately-documented) `_legacy/rel/` package, treat everything in
  *this* document about expression syntax, operators, relationship naming
  (`>>`/`<<`, FK-hint disambiguation), and the JSON-aggregation SQL shape as
  the **semantic baseline that `rel/` inherits** — `rel/` changes the
  surface syntax (JSON instead of a string grammar) and needs its own
  front end for the "unfinished" DML/SQL-execution side, but the underlying
  relational model, resolution algorithm, and SQL-shape strategy in `query/`
  are what to carry forward conceptually.
- Everything in §7 (no built-in ACL, RLS delegated to Postgres via
  `SET LOCAL ROLE` from the JWT) is infrastructure in `pg2.go`/`pg3.go`, not
  in `query`/`rel` themselves, and should be expected to carry over
  unchanged regardless of which front-end syntax the new server adopts.
- The parser bugs in §8 (missing `in`/`like`/`between`/etc. infix handling,
  dead `update` verb) are specific to the *old* string-DSL parser
  (`query/parse2_pratt.go`) that `rel/` does not reuse for parsing (it builds
  expressions from JSON, sidestepping that lexer/parser entirely) — so they
  may already be moot for the rewrite, but they're worth knowing about if any
  future front end resurrects string-based expression parsing.
