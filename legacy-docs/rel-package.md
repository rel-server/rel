# The Legacy `rel` Package

Location: `/home/chris/Code/rel/_legacy/rel/` (Go package `rel`, module `sales-way.com/server`, imported elsewhere as `sales-way.com/server/rel`).

This document is an exhaustive reference on this package's design, behavior, and — importantly — its actual, verified level of completeness, since it is the probable conceptual ancestor of the new server at `github.com/ceymard/rel`.

## 0. Is it live or dead code?

**It is wired into the running server, but it is a debug/diagnostic endpoint, not a functioning query/write engine.**

- `main.go:186` calls `setupPg3Routes(&srv)` inside the `if srv.HasPg() { ... }` block (`main.go:182-190`), alongside `SetupPG(&srv)` and `SetupAuth(&srv)`. This runs whenever the server has a Postgres connection configured — i.e. in normal production/dev operation, not just in tests.
- `setupPg3Routes` (`pg3.go:14-73`) registers `POST /rel2` (`pg3.go:16`). The handler:
  1. Reads the request body (`pg3.go:22`).
  2. Calls `rel.ParseFromBytes(srv.Tables, srv.Functions, body)` (`pg3.go:28`).
  3. Builds a child scope off `srv.RootScope`, calls `rel.NewResolveContext(...)`, `resolveCtx.AddBody()`, and `ops.Resolve(resolveCtx)` (`pg3.go:34-41`).
  4. For each parsed operation, emits a small JSON summary (`relation`, `readonly`, `data_count`, `flat_rows`) and, if the request had a `data` payload, calls `op.FlattenJsonData(body)` (`pg3.go:44-65`).
  5. Writes that summary as the HTTP response, and additionally `pp.Println(ops)` dumps the parsed/resolved operations to the server's stdout for inspection (`pg3.go:69`).

There is **no code path anywhere in this package, or in `pg3.go`, that executes SQL against the database.** The endpoint parses, resolves, and flattens — then just reports counts. This matches its route name `/rel2` (looks like a second, experimental take on `/rel`-style endpoints) and the use of `pp.Println` for developer-facing pretty-printing rather than a real API response shape. Conclusion: **`rel` is a live, reachable, in-progress prototype/spike for a new query+write engine, exercised through a diagnostic HTTP endpoint, not a finished feature.**

## 1. Purpose

`rel` parses a JSON-described "query + optional write" operation, resolves it against database schema metadata (`pg.DBAllTables` / `pg.DBFunctionMap`, the same metadata types used elsewhere in the legacy server, e.g. by `pg/relationships.go`-adjacent code and by the sibling `query` package), and — separately — can take a matching nested JSON *data* payload and flatten it into a list of `(id, parentId, tableId, rowJSON)` tuples suitable (in intent) for a bulk multi-table upsert/merge.

Conceptually it targets the same problem as PostgREST-style nested writes: "give me one JSON document describing a row and its related child rows (e.g. an order with its line items), plus a `write` mode (insert/upsert/merge/update/delete-others) per relation, and the system should persist the whole tree in the right order." The `EffectiveWrite`/mode-inheritance logic (`query_impl.go`) and the parent/child id linkage produced by `flatten.go` are clearly built for exactly this. **However, nothing after "produce flattened rows with parent-child ids and per-node write-mode metadata" is implemented** — see §5 and §8. No SQL is generated, and no write is executed.

It also supports a read-only, "query shape" mode: a `where` clause, a `select` shape (columns/relations/expressions), `rels` (declared joins), `limit`/`offset`. It resolves this against the schema exactly like `query`'s `AstSelection`, but — again — never emits SQL. The `/rel2` handler only reports whether the resolved operation is read-only and how many rows would flatten.

## 2. Input format: JSON, not a string DSL

Confirmed: unlike the sibling `query` package (which has its own lexer/Pratt parser for a *string* DSL — `query/lexer.go`, `query/parse2.go`), `rel` has **no lexer or tokenizer of its own**. Its `from_json*.go` files walk a `github.com/bytedance/sonic/ast.Node` tree (a parsed generic-JSON AST) directly and build `rel`'s own operation/query AST from it. The only overlap with `query` is that `rel` *reuses* `query`'s **expression AST types** (`query.IAstExpression`, `query.AstBinOp`, `query.Identifier`, `query.AstFunctionCall`, `query.AstExpressionList`, `query.AstRawSql`, `query.Scope`, and their `Rewrite`/`Sql`/`String` methods) rather than reimplementing expression modeling — `rel` only reimplements the *front end* (JSON→AST) and the *resolution* (schema binding), not the expression algebra itself.

### 2.1 Top-level shape

Entry point `rel.ParseFromBytes(tables, functions, body)` (`parse.go:17-27`) parses `body` as generic JSON via `sonic.Get`, then calls `parseServerNode` (`parse.go:29-51`):

- A top-level JSON **array** is a batch of operations — each element is parsed independently and results concatenated (`parse.go:31-45`).
- A top-level JSON **object** goes to `parseServerObject` (`parse.go:53-68`), which supports two forms:
  - **Wrapped form**: object has a `"query"` key. Then `"query"`'s value is parsed as a `Query`, and a sibling `"data"` key (if present) is attached as `ServerOperation.Data` (`parse.go:70-80`). This is the form used for writes, since `data` is the row payload to flatten/write.
  - **Bare form**: object has no `"query"` key, so the *whole object* is itself parsed as a `Query` (`parse.go:82-84`, `parseQueryOperation`). This is the shorthand for read-only queries with no `data` payload.
- Anything else at the top level (or nested) is a hard parse error (`"server query must be an object or array"`, `parse.go:49`).

Each parsed operation becomes a `*ServerOperation{ Query *Query; Data *ast.Node }` (`parse.go:10-13`), and `ParseFromBytes` returns a `ServerOperations` slice (`parse.go:15`, i.e. `[]*ServerOperation`).

A `ParsingContext{ Tables, Functions, RelationshipNb int }` (`context.go:5-9`) is threaded through parsing. `RelationshipNb` is a monotonically-increasing counter shared across the *whole* parse (all batch items) — every top-level query (`parse.go:87`) and every nested `rels` entry (`from_json.go:194`) increments it and stamps the resulting `Query.RelIndex` with the new value. This index later becomes the `tableId` tag on every flattened row (see §6) — i.e. it's a "which node in the query tree does this row belong to" discriminator, not a schema/table id.

### 2.2 The `Query` JSON object

`Query.FromNode` (`from_json.go:131-241`) accepts **only** the following keys (any other key is a hard error — `"unknown field in query: "+pair.Key`, `from_json.go:236`, so the schema is closed/strict):

| Key | Meaning | Parsed by |
|---|---|---|
| `relation` | table/view identifier to query | `Identifier.FromNode`, `from_json.go:11-36` |
| `arguments` | arguments to call a **function** instead of a table (see below) | `parseArguments`, `from_json.go:243-281` |
| `write` | write-mode block (see §2.3) | `Write.FromNode`, `from_json.go:56-106` |
| `where` | filter expression (any JSON expression form, see §2.4) | `ExpressionFromNode`, `from_json_expression.go:23-50` |
| `select` | field/relation selection shape (see §2.5) | `selectFieldsFromNode`, `from_json_select.go:12-25` |
| `on` | join column mapping — **only legal inside a `rels` entry** (`"on can only be used in a rel"` error otherwise, `from_json.go:169-171`) | inline, `from_json.go:172-187` |
| `rels` | map of alias → nested `Query`, each treated as a join/relation to the parent | inline, `from_json.go:188-209` |
| `on_conflict` | string array, upsert conflict target columns | `getStringSliceFromNode` |
| `insert_columns` / `insert_only` | string array (both keys are aliases of each other) | ditto |
| `update_columns` / `update_only` | string array (both keys are aliases) | ditto |
| `offset`, `limit` | integers | `parseIntNode`, `from_json.go:283-294` |

**`relation` (`Identifier`)**: either a bare JSON string (used as `Name`, no schema — `from_json.go:15-16`), or an object with keys **`schema`, `db`, `name`** (`from_json.go:17-31`); any other key inside that object is an error. `Identifier.TableKey()` (`query.go:27-35`) prefers `schema.name`, falls back to `db.name`, else bare `name` — matching the `"schema.table"` keying convention of `pg.DBAllTables` (confirmed by the test's `tables["api.items"]`, `parse_test.go:55`).

  > **Verified defect**: `parse_test.go:64` and `:69` write the inner identifier object as `{"schema": "api", "relation": "items"}` — using `relation` as the inner key. The implementation only recognizes `schema`/`db`/`name` and treats any other key as an error. As committed, `go test ./rel/...` **fails** with `unknown field: relation` (confirmed by running it). See §7/§8.

**`arguments`**: presence of this key means the query targets a **function**, not a table — resolution looks it up in `ctx.Functions` instead of `ctx.Tables` (`query_resolve.go:70-80`). Can be a JSON array (positional args) or object (named args, each becomes a `query.AstNamedFunctionArgument`) (`from_json.go:243-281`).

**`rels`**: object mapping alias → nested query object. Each nested query is marked `IsRel: true`, gets its own `RelIndex`, and its `Alias` is set to the map key (`from_json.go:193-206`). Its `on` map (`from_json.go:168-187`) declares `{localColumn: distantColumn, ...}` — this is later matched against real foreign-key metadata during resolution (§4), it is **not** itself the source of truth for the join.

  > **Verified defect**: `parse_test.go:66` uses the key `"rel"` (singular) for this nested-relations map. The implementation only recognizes `"rels"` (plural, `from_json.go:188`). As committed this is a second reason `go test ./rel/...` fails (`unknown field in query: rel`) until fixed. See §7/§8.

### 2.3 The `write` JSON object

`Write.FromNode` (`from_json.go:56-106`), also closed-schema, accepts `relation`, `mode`, `on_conflict`, `insert_columns`/`insert_only`, `update_columns`/`update_only`. `mode` is a string mapped by `parseWriteMode` (`from_json.go:108-129`) to a `WriteMode` enum (`query.go:8-19`):

```
readonly | insert | upsert | merge | mergenew | merge-update/mergeupdate | update | deleteonly/deleteother
```

Note there is **no plain "delete"** mode — only `deleteonly`/`deleteother`, mapping to `MODE_DELETEOTHER`. This models "delete rows that are absent from this write's payload" (an orphan-pruning / sync semantic for nested child collections), not "delete the row(s) matched by a filter." This is consistent with the nested-write use case (e.g. "replace this order's line items with exactly this set") but means a bare row-level delete-by-filter is not something this vocabulary expresses at all.

If a `Query` has no explicit `write` block, `EffectiveWrite()` (`query_impl.go:7-40`) synthesizes one from the query's own top-level `relation`/`on_conflict`/`insert_only`/`update_only` fields, defaulting to `MODE_READONLY`. Crucially, if a query is still read-only after that, and it has a `ParentQuery`, it **inherits its parent's effective write mode and column-lists** (`query_impl.go:20-36`) — so setting `write.mode: "merge"` on the top-level query cascades that mode down to every relation nested under it, unless a relation overrides its own `write` block. `IsReadOnly()` (`query_impl.go:3-5`) is just `EffectiveWrite().Mode == MODE_READONLY`.

### 2.4 Expression / `where` grammar

Expressions are JSON values interpreted recursively by `ExpressionFromNode` (`from_json_expression.go:23-50`):

- `null` / `true` / `false` / number → `query.AstRawSql("null"/"true"/"false"/<raw number text>)`.
- string → `query.Identifier{Identifier: <string>}` — i.e. **bare strings are always column/symbol references**, never string literals, in expression position. (There is no JSON-level "string literal" — this mirrors SQL-ish identifier-by-default semantics, presumably resolved against the query's scope later.)
- array → operator/function-call form (see below).
- object → becomes a `query.AstExpressionList` of `AstNamedFunctionArgument`s, one per key/value pair (`expressionFromObject`, `from_json_expression.go:52-73`) — used e.g. for named function arguments.

Array form `[op, ...args]` (`expressionFromArray`, `from_json_expression.go:75-106`): if the first element is a non-empty string, it's an operator name, dispatched by `expressionFromOperator` (`from_json_expression.go:108-255`). If the first element is not a string (or is an empty string) and there's exactly one element, it's just that element's expression (parenthesization). If there is no leading operator string and more than one element, it's an `AstExpressionList` (comma-list). An **empty array without a leading operator** is a parse error (`"empty expression array"`, `:81`) unless it appears at the *field-selection* layer where `[]` after a `"*"` key or an empty array alias means "star with no exclusions" (see §2.5).

Recognized operators (case-insensitive, `from_json_expression.go:108-255`):

| Operator(s) | Arity | Produces |
|---|---|---|
| `-`, `not`, `~` | 1 | `query.AstPrefixOp` |
| `between`, `not between` | 3 (min, value, max) | rewritten as `value BETWEEN min AND max` via nested `AstBinOp` |
| `concat`, `concat_ws`, `coalesce`, `arr`/`array`, `lst`/`list` | ≥1 | `query.AstFunctionCall{Left: Identifier(op), Arguments: args}` |
| `format` | ≥1, first is a literal format string | `AstFunctionCall{Left: Identifier("format"), Arguments: [RawSql(format), ...rest]}` |
| `call` | ≥1, first is a function identifier expression | `AstFunctionCall{Left: <first>, Arguments: rest}` — arbitrary function call by name/expression |
| `agg`/`aggregate` | ≥2 (id, args[, filter]) | `AstFunctionCall{Left: id, Arguments: aggArgs[, filterExpr]}` — models `agg(expr) FILTER (WHERE ...)`-style aggregates |
| `col`, `get`, `set` | any | `AstFunctionCall{Left: Identifier(op), Arguments: args}` — **valid as expressions but only meaningfully resolved in `select` context** (comment at `from_json_expression.go:221`) |
| `*` | any (rest are exclusion column names) | `AstFunctionCall{Left: Identifier("*"), Arguments: <string identifiers>}` — star-with-exclusions, meaningful in `select` context |
| *(anything else, e.g. `=`, `>=`, `and`, `or`, `like`, `+`, ...)* | exactly 2 | generic `query.AstBinOp{Op: op, Left, Right}` — the fallback path (`from_json_expression.go:243-254`) |

Example from the test (`parse_test.go:66`): `"where": [">=", "id", 1]` → `AstBinOp{Op: ">=", Left: Identifier("id"), Right: RawSql("1")}`, i.e. `id >= 1`.

### 2.5 `select` grammar

`selectFieldsFromNode` (`from_json_select.go:12-25`):

- JSON **object**: each key is a field alias, each value a field spec — recursed via `selectFieldFromNode` (`:27-56`). If a value is itself an object, it becomes a nested `AstFieldGroup{Name: alias, Fields: [...]}` (JSON-nested column groups, e.g. selecting into a sub-object — ties into the `->` JSON-path prefixing done at resolve time, §4). Otherwise it's parsed as a scalar/array field spec.
- Bare **array/string/number/bool/null** at the top of `select` (not inside an object): treated as a single implicit field with alias `""` (`from_json_select.go:16-21`).

Scalar/array field spec (`selectFieldFromExpression`, `:58-142`):
- `[]` (empty array) with alias `""` or `"*"` → `AstStarSelector{}` (all columns, no exclusions).
- Plain string `"col"`: if it has an alias, produces `AstSimpleField{Name: alias, Expression: Identifier(col)}`; if no alias, `AstSimpleField{Name: col, Expression: Identifier(col)}` (self-aliased column reference). This is how the test's `"tags": "tags"` select entry (`parse_test.go:75`) works: alias `"tags"` mapped to identifier `"tags"`, later recognized as a **relation reference** during resolution (§4) because `"tags"` matches a declared `rels` alias.
- `["*", "excl1", "excl2", ...]`: `AstStarSelector{Name: alias, Exclusions: {excl1, excl2}}` — star selector excluding named columns. The test's `"*": []` (`parse_test.go:74`) is exactly this with zero exclusions.
- `["col"/"get"/"set", colname, ...]`: `selectFieldFromColOp` (`:144-180`). For `"get"`, this is special-cased to collapse straight to `AstSimpleField{Name: alias-or-colname, Expression: Identifier(colname)}` (extra args are parsed but discarded for `get`) — i.e. `["get","name"]` ≈ selecting `name` directly. For `"col"`/`"set"`, it builds an `AstFunctionCall` retaining all arguments (their exact resolved meaning is deferred to consumers; nothing in this package interprets `set`'s extra semantics beyond recording it, see §8).
- Any other leading operator string, or a non-array/non-string scalar: parsed via the generic expression grammar (§2.4) and wrapped in `AstSimpleField{Name: alias, Expression: expr}` — **requires an explicit alias** (`"expression field requires an alias"` error if alias is empty, `:128`/`:139`).

## 3. The AST model (`ast.go`)

`ast.go` defines only the **field-selection** AST (the expression AST is imported wholesale from `query`, see §2). Four field-node kinds implement `IAstField` via a visitor pattern (`ast.go:9-65`):

- `AstFieldGroup{Name string; Fields []IAstField}` — a named nested group of fields (renders as a nested JSON sub-object in output; used for `col`-group-style JSON-path selections).
- `AstStarSelector{Name string; Exclusions utils.Set[string]}` — "all columns" (optionally under a named sub-key), minus an exclusion set.
- `AstSimpleField{Name string; Expression query.IAstExpression}` — one aliased scalar expression/column.
- `AstRelationshipField{Alias, RelAlias string; IsReadOnly bool; Query *Query; ResolvedRelationShip *pg.RelationShip}` — a reference to a declared relation (`rels` entry) appearing in the select list; carries a pointer to the (resolved) nested `Query` and to the matched FK metadata once resolved.

`IAstFieldVisitor` (`ast.go:44-49`) has one `Visit*` method per node kind; this is the same visitor shape as `query/ast.go`'s field AST (structurally mirrored — `rel` appears to have been bootstrapped by copying `query`'s field-AST pattern and adapting it for JSON-driven construction). Two consumers implement this visitor: the resolver (`query_resolve.go`, via direct type-switch rather than the visitor interface — see §4) and the flattener (`flatten.go`'s `jsonLocalBuilder`, see §6, which *does* use the visitor interface).

## 4. Resolution (`context.go`, `resolve_context.go`, `query_resolve.go`)

"Resolving" here means: binding every parsed identifier/relation to real schema metadata (`pg.DBTable`, `pg.RelationShip`, `pg.Function`), validating shapes that require schema knowledge (e.g. "does this table have a relationship matching this `on` map?"), and rewriting every expression against a `query.Scope` so that plain identifiers become resolvable symbols. **It does not typecheck value types, and it does not produce SQL or any executable plan** — the output is still an in-memory Go struct tree (`*Query`, with new fields populated), not a query string or bytecode.

### 4.1 Context types

- `ParsingContext{ Tables pg.DBAllTables; Functions pg.DBFunctionMap; RelationshipNb int }` (`context.go:5-9`) — used only during the JSON→AST parse phase (§2), not during resolution.
- `ResolveContext{ Tables pg.DBAllTables; Functions pg.DBFunctionMap; scope *query.Scope }` (`resolve_context.go:8-12`), created via `NewResolveContext(tables, functions, root *query.Scope)` (`:14-20`). `.Child()` (`:22-28`) derives a context with a child `query.Scope` (`scope.Child()`), used per query level so nested relations get their own lexical scope layered on the parent's. `AddBody()` (`:30-32`) registers a special symbol `$body` rewritten to SQL `($1::jsonb)` in the scope — a hook for referencing the raw request-body JSON parameter in expressions (e.g. write-time expressions referencing the incoming payload), though nothing in this package actually consumes `$body` beyond registering it.

This is a near-duplicate, cut-down version of `query.ResolveContext` (`query/context.go:17-41`, same shape and same `NewResolveContext(tables, functions, root)`/`Child()`/`AddBody()` signatures) — `rel` did not import/reuse `query`'s resolve context, it re-implemented an equivalent one. External dependency: both need the caller to supply already-introspected schema metadata (`pg.DBAllTables`, `pg.DBFunctionMap`), i.e. this package assumes some other component (in the live server, populated by `pg`'s DB introspection — see `srv.Tables`/`srv.Functions` in `pg3.go:28`) has already discovered tables/columns/relationships/functions from Postgres and handed them over as data. `rel` itself never talks to the database.

### 4.2 Resolution algorithm (`query_resolve.go`)

Entry points:
- `ServerOperations.Resolve(ctx *ResolveContext) error` (`:12-19`) — resolves every operation's `Query` with no parent.
- `(*Query).Resolve(ctx *ResolveContext, parent *Query) error` (`:21-65`) — per-query steps, in order:
  1. `q.ParentQuery = parent`.
  2. `resolveRelation(ctx)` (`:67-94`): if `q.Arguments != nil`, treat `q.Relation.TableKey()` as a function name, look up in `ctx.Functions`; if the function `ReturnsComposite`, additionally resolve its return type as a `pg.DBTable` (so its output can still be selected/shaped like a row). Otherwise look up `q.Relation.TableKey()` in `ctx.Tables`. Missing table/function ⇒ hard error (no fallback, no fuzzy matching).
  3. Derive a child `ResolveContext` (new nested `query.Scope`).
  4. `populateScope(childCtx)` (`:96-117`): for the resolved table, register every **computed column** as a rewritten symbol expanding to `schema.func(ROW("alias".*)::table)(...)` (`:102-106` — a Postgres row-expression call convention for computed/generated columns), register every **plain column** as a simple symbol, and register every declared `rels` alias as a simple symbol (so `"tags"` can appear bare in a `where`/`select` and resolve as a relation reference, not a column).
  5. For every entry in `q.Rels`, call `resolveAsChild(childCtx, q, alias)` (`:129-161`): resolves the child's own table, then `findRelationship(parent.ResolvedTable, rel.ResolvedTable, rel.On)` (`:163-186`) — scans `parent.ResolvedTable.Relationships` for FK relationships pointing at the child table whose column-pairs exactly match the JSON `on` map (`relationshipMatchesOn`, `:188-199`; count and column identity must both match, else "ambiguous" or "no relationship matches" errors). This is how the declared `on: {"id": "item_id"}` in the test is checked against real FK metadata rather than trusted blindly. The matched `*pg.RelationShip` is stored on the child query; the parent's `OutgoingsRels` or `IncomingsRels` list is appended to depending on `relationship.IsReverse` (child recursively resolves itself with the parent, on the same context, before this classification).
  6. `resolveSelect(childCtx)` (`:201-235`): if `q.Fields` is empty (no explicit `select` in the JSON), synthesizes a default `[*Star, ...one AstRelationshipField per declared rel]`, sorted so plain fields sort before relationship fields (`:217-221`, a stable partition, not a full sort by name). Otherwise walks every already-parsed field via `resolveField` (`:237-318`), a **direct type switch** (not through the `IAstFieldVisitor` interface, unlike `flatten.go`) that:
     - Resolves `AstRelationshipField.RelAlias` against `q.Rels`, attaching the resolved child `*Query` and its `IsReadOnly()` flag.
     - Recurses into `AstFieldGroup`, building up a Postgres JSON-path prefix string (`->'name'`) as it descends — this prefix is stashed on every leaf as `ResolvedSimpleField.JsonPathPrefix`, clearly intended for later "extract this column from this nested JSON path" SQL generation that is not implemented here.
     - For `AstStarSelector`, expands to one `ResolvedSimpleField{Name, JsonPathPrefix}` per column in `ResolvedTable.ColumnsInOrder`, skipping exclusions.
     - For `AstSimpleField`: if its expression is a bare `Identifier` that happens to match a `q.Rels` alias, it's **retroactively promoted to an `AstRelationshipField`** (this is exactly how `"tags": "tags"` in the test's `select` becomes a relationship field, not a literal column named `tags`). Otherwise the expression is rewritten against the scope (`Expression.Rewrite(scope)`, from the `query` package) and, if it resolves to a real column identifier / raw-SQL literal / `col`-style function call (`columnFromColOp`, `:320-332`), a `ResolvedSimpleField` is recorded.
  7. `q.Where`, if present, is rewritten against the (child) scope (`Where.Rewrite`).
  8. Every `q.Arguments` expression (function-call args, if this query targets a function) is likewise rewritten.

Net effect: after `Resolve`, a `Query` carries `ResolvedTable`/`ResolvedFunction`/`ResolvedRelationship`, `OutgoingsRels`/`IncomingsRels` (classified by FK direction), and `ResolvedSimpleFields` (flattened list of concrete columns-to-select with their JSON-path prefix) — all the bindings a SQL generator would need, but `rel` stops short of actually generating SQL (§5, §8).

## 5. Query execution / output — public API surface

There is **no execution** in this package at all — no DB calls, no SQL string building. The full, exhaustive public API surface is:

- `rel.ParseFromBytes(tables pg.DBAllTables, functions pg.DBFunctionMap, body []byte) (ServerOperations, error)` (`parse.go:17`) — JSON bytes → parsed AST (`ServerOperations = []*ServerOperation`).
- `rel.NewResolveContext(tables pg.DBAllTables, functions pg.DBFunctionMap, root *query.Scope) *ResolveContext` (`resolve_context.go:14`).
- `(*ResolveContext).AddBody()` (`resolve_context.go:30`) — registers the `$body` scope symbol.
- `(ServerOperations).Resolve(ctx *ResolveContext) error` (`query_resolve.go:12`) — resolves every parsed operation in place.
- `(*ServerOperation).FlattenJsonData(body []byte) ([][]any, error)` (`flatten.go:142`) — takes the *raw* original request bytes (re-parses them; ignores any already-cached `op.Data` unless present — `flatten.go:149-157`) and flattens the row tree into `[][]any` rows shaped `[id int64, parentId int64, tableId int, rowJSON []byte]` (see §6).
- `(*Query).Resolve(ctx *ResolveContext, parent *Query) error` (`query_resolve.go:21`) — per-query resolution, callable directly.
- `(*Query).IsReadOnly() bool` and `(*Query).EffectiveWrite() Write` (`query_impl.go:3`, `:7`) — write-mode inheritance/query.
- `(*Identifier).TableKey() string` (`query.go:27`).

So the pipeline this package implements is exactly: **JSON → AST → resolved AST (bound to schema) → (optionally) flattened `(id, parentId, tableId, json)` rows**. Nothing downstream of that (SQL text, prepared statements, actual persistence, actual result-set construction for reads) exists in `rel`. Contrast with `query`, which continues on to `SqlSelect`/`sqlSelectQuery` (`query/sql_constructs.go:39,172`) and actually emits SQL text — `rel` has no equivalent of that file at all.

## 6. `flatten.go`: JSON *data* payload → flat rows (write-direction, not read-direction)

This is the reverse of "flatten nested SQL rows into nested JSON for a response" — here, flattening runs over a **nested JSON data payload supplied by the client** (the `data` sibling of `query` in the wrapped request form, §2.1) and walks it in lock-step with the already-resolved `Query`/`Fields` tree, emitting one output row per JSON object encountered, each tagged with its own id, its structural parent's id, and which node of the query tree (`RelIndex`) it came from.

- `flatContext{ lastId int64; nodes [][]any }` (`flatten.go:10-13`) accumulates output rows; `nextId()` is a simple incrementing counter (**process-local, resets to 0 per call, not a DB sequence or globally unique id** — `flatten.go:15-18`).
- `jsonLocalBuilder` (`flatten.go:27-33`) implements `IAstFieldVisitor` against a **live JSON node** (`current *ast.Node`) rather than against schema metadata: it walks `sel.Fields` (the resolved field list) and, for each field, tries to pull the correspondingly-named key out of the current JSON object (`current.Get(name)`), threading matches through a `cbk` callback that just appends `ast.Pair`s into a local list.
  - `VisitFieldGroup`: descends into `current.Get(group.Name)` and recurses with the same builder shape (`:35-53`) — reconstructs nested JSON sub-objects verbatim rather than flattening them further (a field group stays nested in the *output* row's JSON blob; only *relationship* fields get pulled into separate flat rows).
  - `VisitStarSelector`: copies every non-excluded column of `sel.ResolvedTable.ColumnsInOrder` straight from the current JSON object into the output pairs, if present (`:55-67`) — i.e. it trusts the resolved table's column list, not the field's own (already-resolved) `ResolvedSimpleFields`.
  - `VisitSimpleField`: copies one named key verbatim (`:69-80`).
  - `VisitRelationshipField` (`:82-115`): this is where actual flattening happens. If `field.Query == nil`, error ("relationship field %s is not resolved") — i.e. flattening a payload **requires that `Resolve` has already run** on the query (there is no independent flatten-without-resolve path). If `field.IsReadOnly` (the nested relation's effective write mode is `MODE_READONLY`), the nested data is **skipped entirely** — read-only nested relations are not flattened into separate rows (nothing to write, so no point emitting them). Otherwise, the corresponding JSON value under `field.Alias` must be a JSON array (each element flattened as a child, via `field.Query.FlattenJsonDataItem`, passing `builder.parentId` as `parentId`) or a JSON object (flattened as a single child) — anything else is an "invalid node type" error.
- `(*Query).FlattenJsonDataItem(item *ast.Node, parentId int64, ctx *flatContext) error` (`:117-140`): allocates a new id for *this* JSON object (`ctx.nextId()`), builds up its own "shallow" pairs (columns + nested field-group JSON, but *not* nested relationship JSON, which was hived off into separate rows above as children keyed by this new id as their `parentId`), re-serializes those pairs into one flat `ast.NewObject(pairs)` JSON blob, and records `[myId, parentId, sel.RelIndex, jsonBytes]` into `ctx.nodes`.
- `(*ServerOperation).FlattenJsonData(body []byte) ([][]any, error)` (`:142-179`) is the outward-facing entry point: parses `body` fresh via `sonic.Get` (ignoring any `op.Data` set during earlier parsing unless `op.Data != nil`, in which case that cached node is reused instead — `:149-157`), then handles a top-level JSON array (one call to `FlattenJsonDataItem` per element, all with `parentId = 0`) or a single top-level object (`parentId = 0`), returning the accumulated `ctx.nodes`.

**In the parse_test.go example** (`parse_test.go:78`, `"data": [{"id": 1, "name": "a", "tags": [{"tag": "x"}]}]`): the top-level array has one item → id 1 gets `{id:1,name:"a"}` (star-selected columns only; `tags` is a relationship field, hived off) as row 1 with `parentId=0`, `tableId=<top query's RelIndex>`; its one `tags` child `{tag:"x"}` becomes row 2 with `parentId=1`, `tableId=<tags rel's RelIndex>`. The test asserts exactly `len(flats) == 2` (`parse_test.go:110-112`), confirming this shape.

This design strongly suggests the intended next stage (not present in this package) was: take these `(id, parentId, tableId, json)` tuples, `INSERT`/`COPY` them into some staging table or CTE keyed by `(id, parentId, tableId)`, then run per-`tableId` (i.e. per query-tree-node) upsert/merge/delete-other statements in parent-to-child (or child-to-parent, for FK ordering) order, using each node's `EffectiveWrite()` mode and `On` mapping to know how to join staged children back to their parents. None of that exists yet.

## 7. Test coverage (`parse_test.go`)

There is exactly **one** test, `TestParseAndResolveQuery` (`parse_test.go:61-113`), exercising the full pipeline end-to-end: JSON parse → `Resolve` → `FlattenJsonData`, against a hand-built two-table schema (`testTables()`, `:10-59`: `api.items` ←(reverse, multiple)— `api.items_tags` via `item_id`).

What it *would* verify, if it passed as committed:
- A query object with `relation`, `write.mode = "merge"`, a `where` clause (`[">=","id",1]`), a nested `rels.tags` relation joined via `on: {"id":"item_id"}`, and a `select` of `{"*":[],"tags":"tags"}` parses without error.
- After `Resolve`, `q.ResolvedTable.Name == "items"`, exactly one incoming relationship is recorded (`len(q.IncomingsRels) == 1`, consistent with the FK being `IsReverse: true` in the fixture), and `q.Where` is non-nil.
- `FlattenJsonData` on the accompanying `data` array produces exactly 2 flat rows (1 parent + 1 child tag), matching the analysis in §6.

**As actually committed, this test fails** (verified by running `go test ./rel/...`): it errors `unknown field: relation` because the fixture's `relation` objects use an inner key `relation` (`parse_test.go:64,69`) where the implementation (`from_json.go:11-36`) requires `name`. Locally patching that one key reveals a **second** mismatch: the fixture's `rel` key (`parse_test.go:66`) is not `rels` as required by `from_json.go:188`. With **both** typos fixed (`relation`→`name` inside the identifier object, `rel`→`rels` for nested relations), the test passes cleanly (verified locally, changes not committed/kept). This means the actual behavior implemented in the non-test files is internally consistent and does work for this scenario — it's specifically the test fixture that has drifted out of sync with the parser's expected JSON keys, which is itself telling: nobody has run `go test ./rel/...` successfully in a while, or the JSON vocabulary changed after the test was last touched.

No other test file exists for this package — nothing exercises `arguments`/function-call queries, `col`/`get`/`set` field ops, computed columns, ambiguous-relationship errors, multiple write modes, `on_conflict`/`insert_only`/`update_only`, `offset`/`limit`, or batch (array-of-operations) input. Everything beyond the single fixture above is **unverified by any automated test**.

## 8. Completeness assessment

**Solidly implemented** (works, has at least indirect verification):
- JSON→AST parsing for the full documented vocabulary (§2) — closed/strict key sets, sensible error messages for unknown keys.
- Expression sub-grammar (§2.4) reusing `query`'s expression algebra — no gaps found in the operator dispatch table.
- Schema resolution: table/function lookup, FK-relationship matching by exact column-set (`findRelationship`), scope population including the computed-column row-expression convention, default `select *` + auto-added relation fields when no `select` is given, write-mode inheritance (`EffectiveWrite`).
- Data-payload flattening in lock-step with the resolved field tree, correctly skipping read-only relations and correctly threading parent/child ids and per-node `RelIndex`.

**Explicitly stubbed / not implemented at all** (no code, not even an error stub — the feature is simply absent):
- **SQL generation.** No `Sql`/`ToSql`/equivalent method exists anywhere in this package for `Query`, `Write`, or the field ASTs — despite `ResolvedSimpleField.JsonPathPrefix` and every resolved binding being clearly shaped for exactly that purpose. Compare to `query/sql_constructs.go`'s `SqlSelect`, which has no counterpart here.
- **Write execution.** `WriteMode` values (`insert`/`upsert`/`merge`/`mergenew`/`merge-update`/`update`/`deleteonly`) are parsed, inherited, and exposed via `EffectiveWrite`, but nothing ever branches on them to build an `INSERT`/`UPDATE`/`DELETE` statement or call the database. They are pure metadata at this stage.
- **Function-call queries end-to-end**: `resolveRelation` resolves `arguments`-bearing queries against `ctx.Functions` and even resolves a composite return type back to a table (`query_resolve.go:70-80`), but there is no code path that actually invokes a Postgres function or shapes its result — again, resolution-only.
- **`Query.Select query.IAstExpression`** (`query.go:55`) is a dead field: grepping the whole package, it is never assigned nor read anywhere. It looks like an abandoned earlier design (a single top-level "select expression" instead of the `Fields []IAstField` list that is actually used).
- **`$body` scope symbol** (`resolve_context.go:31`) is registered but never referenced by any expression-building code in this package — presumably meant to let `where`/`write`-related expressions reference the raw incoming JSON body as a bound SQL parameter, but nothing wires that up yet.
- **`col`/`set` field operators** (`from_json_select.go:119-120,163-179`) are parsed into `AstFunctionCall` nodes and even get a `columnFromColOp` recognizer during resolution (`query_resolve.go:320-332`), but their extra arguments (beyond the column name) are never interpreted by anything in this package — likely meant for JSON-path/JSON-merge write semantics (`set`-into-a-JSON-column?) that don't exist yet.
- **No `MODE_DELETE`** (plain filtered delete) exists at all, only "delete-other" (orphan pruning) — see §2.3. If a plain delete-by-filter is a desired capability, this vocabulary has no slot for it yet.

**Currently broken as committed** (not just incomplete — actually inconsistent with its own test):
- `go test ./rel/...` fails outright (verified) due to the two key-name mismatches in `parse_test.go` described in §7 (`relation` vs `name`; `rel` vs `rels`). This is the single piece of automated verification this package has, and it does not pass in the repository's current state.

**No panics, no "not implemented" error strings, no empty function bodies were found** anywhere in the package (grepped for `TODO`, `FIXME`, `panic(`, `not implemented`) — the code that exists is written carefully and defensively (closed schemas, explicit error messages for every unrecognized shape), it just terminates earlier in the pipeline (at "resolved AST") than a working query engine needs to.

## 9. Notes / things to reconsider for the rewrite

**Genuinely reusable ideas / shapes:**
- The **closed-JSON-schema parsing style** (every object key explicitly matched in a switch, unknown key ⇒ hard error) is a good discipline to keep — it makes the accepted vocabulary self-documenting and fails loudly on typos (ironically, this is exactly the discipline that would have caught the test's own two key-name mistakes immediately had the test been run in CI). Worth carrying forward as a convention in the new server, along with actually running the test suite before treating a JSON contract as stable.
- **Resolving `rels`/joins by matching a declared `on` column-map against real introspected FK metadata** (`findRelationship`/`relationshipMatchesOn`) rather than trusting client-declared joins blindly is a solid, safety-conscious pattern — it prevents a client from asking the server to join across columns that aren't an actual foreign key relationship, and naturally disambiguates when a table has multiple FKs to the same target (by requiring the full column set to match). This is worth preserving as a principle, though the "must specify all columns of one exact FK, else ambiguous" UX could be made friendlier (e.g. accept an explicit named-constraint reference as an alternative to column-mapping, for tables with genuinely ambiguous multi-column FKs).
- The **write-mode inheritance model** (`EffectiveWrite`: unset write blocks default to the query's own relation/columns, and cascade the *parent's* write mode down to children lacking their own) is a clean way to let a client say "merge this whole tree" once at the root instead of repeating `write: {mode: merge}` on every nested relation. Good design to keep, provided the "no plain delete, only delete-other" gap (§8) is deliberately revisited — decide explicitly whether row-level delete-by-filter needs its own mode/vocabulary in the new server, rather than carrying forward the omission unexamined.
- The **flatten-to-`(id, parentId, tableId, json)` tuples** approach to turning an arbitrarily nested write payload into a linearized, parent-tracked list is a reasonable intermediate representation for a bulk multi-table write (e.g. to drive a single multi-row `INSERT ... FROM jsonb_to_recordset` per table, joined back via the synthetic parent ids) — but note the ids are **process-local sequence numbers, not stable/globally-unique**, so any design carrying this forward needs to decide how those ids get threaded through to real FK values during the actual write (this package stops exactly at the point where that would need to be decided).
- Reusing an existing expression AST/algebra (rather than re-deriving one) for a JSON-driven front end is sound; if the new server keeps a string-DSL parser (like `query/`) *and* wants a JSON front end (like `rel/`), sharing one expression AST + `Rewrite`/`Sql` implementation between both front ends — as `rel` already does by depending on `query`'s expression types — is the right call and should probably be made an explicit, first-class shared module rather than an implicit cross-package dependency the way it is today.

**Likely dead ends / things not to carry forward as-is:**
- **Do not carry forward the fact that this stops at "resolved AST."** The most important open question this package leaves unanswered is exactly the part a rewrite most needs designed: how resolved reads become SQL (`query/` already answers this — study that instead), and how resolved + flattened writes become actual multi-table SQL statements executed in the correct dependency order with real conflict/merge handling. There is no prior art for either in this package to lean on; it would need to be designed fresh.
- **`ResolveContext`/`ParsingContext` here largely duplicate `query`'s equivalents** (`query/context.go`) with the same method names and signatures. In a rewrite, this suggests unifying into one shared resolution-context type used by both a JSON front end and a string-DSL front end (if the new server keeps both), rather than maintaining two near-identical copies as this legacy tree does.
- **The dead `Query.Select` field** and the **unwired `$body` symbol** are signs of at least one earlier design iteration that was abandoned mid-flight; don't assume every field on `Query` reflects a deliberate, current design — cross-check each field against actual read/write sites (as this document did) before treating it as meaningful prior art.
- **The `col`/`get`/`set` operator family** feels like an under-specified attempt at JSON-path-style nested field access/mutation bolted onto a column-oriented select grammar; `get` collapses to a plain column reference and `col`/`set` don't do anything beyond being recorded, which suggests this was aspirational rather than settled design. If the new server wants JSON-path field access/patch semantics, it's probably worth designing that vocabulary from scratch rather than inheriting this particular shape.
- **`RelIndex`/global counter as the only per-node discriminator** in the flattened output is minimal and workable for a prototype, but conflates "identity of a node in the parsed query tree" with what would eventually need to also carry "which physical table/relation" for SQL generation (right now that's *recoverable* by cross-referencing back into the resolved `Query` tree via the counter, but only because the whole `ServerOperations` tree is kept in memory alongside the flattened rows — a real implementation would want a more self-contained row-tagging scheme, e.g. tagging rows with resolved table identity directly rather than an opaque tree-position counter).

**Overall verdict**: this package is best read as a **spike validating that a strict, closed JSON vocabulary can be parsed and resolved against real schema/FK metadata, and that a nested write payload can be linearized with parent-tracking** — not as a load-bearing implementation to port forward wholesale. The parsing and resolution *disciplines* (closed schemas, FK-verified joins, write-mode inheritance) are worth keeping; the *vocabulary specifics* (especially around `col`/`set`, delete semantics, and the unfinished write path) should be treated as an early draft to be redesigned with the benefit of hindsight, not as a frozen contract.
