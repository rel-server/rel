# Query compiler : pass 1 / pass 2 design

Tracks the `query` package's compiler architecture as it's being decided — see `specs/TODO.md`'s "Query compiler architecture" entry, which this exists to close. Durable rules settled here should eventually migrate into `querying.md` proper ; this file is where they get worked out first, across sessions.

## Passes

Compiling a query JSON tree happens in two passes.

**Pass 1 — tree inflation + DB resolution. Implemented** (`query/node_parse.go`'s decode step, `query/node_resolve.go`'s resolve step, plus `pg/info_searchpath.go` and `pg/info_lookup.go` for the search-path/by-name lookups it needed that `pg` didn't have yet — see those files' tests for coverage). Builds the `QueryNode` tree from JSON and resolves every DB-facing reference against `pg` introspection : `relation`/`schema`/`arguments` -> `*pg.Relation`/`*pg.Function`, `on` -> `ResolveJoin`, `on_conflict` -> a real constraint. The relation/function-as-root blacklist check belongs here, at the point each node's own relation/function is resolved — not in pass 2.

A function-rooted node also gets `Relation` populated (via `GetRelationByType` on its return type, nil if the return type isn't a known relation) — decided during implementation, since a table-valued function is joinable/writable exactly like the type it returns. `QueryNode`'s "either Relation or Function" doc comment was updated accordingly : `Function != nil` decides *kind*, `Relation` is what descendants/writes resolve against, and a function node legitimately has both set.

> Why: a node's own relation/function is resolved against `pg` the same way `on`/`on_conflict` are, not against an `Expression` tree, so its blacklist check naturally sits next to that resolution rather than being duplicated into expression walking.

Expression-typed fields (`where`, `select`, `order_by`, `distinct_on`, function `arguments`) are parsed (`query/expression_parse.go`) but not resolved during pass 1 — bare strings stay opaque `Identifier` nodes.

**Pass 2 — expression resolution.** Two steps, not one :

1. **Resolution** : a generic walk, uniform across every Expression-typed field, that binds each bare identifier to a column/alias/param and blacklist-checks every `call`/`agg` identifier against `config.Blacklist`. Resolves *in place* — mutates the existing `Expression` tree rather than building a second, parallel `ResolvedExpression` tree isomorphic to the first.

   > Why: a whole shadow tree duplicates memory for no benefit here — the original unresolved tree isn't needed again once resolution has run, so there's nothing gained by keeping both.

   Resolved values live in mutable fields added to the node types that need them (`Identifier`, `AggExpr`/`CallExpr`'s `FunctionRef`, `GetExpr`/`SetExpr`/`GetSetExpr`'s `Column`) — not a second node type substituted in during resolution. This requires those node types to be held as pointers (`*Identifier`, not `Identifier`) wherever they sit in the tree, so a mutation made through a type-switched `*Identifier` is visible everywhere else that same node is referenced — currently `query/expression_parse.go` returns every node by value ; switching the constructors to return pointers is the concrete follow-on work this decision implies, not yet done.

2. **Shape/writability derivation**, over the already-resolved `select` tree only — `where`/`order_by`/`distinct_on`/etc. don't produce an exported shape or writable columns, so this step doesn't touch them. Produces, per node : the exported shape (`query/resolved_field.go`'s `ResolvedField`), the extractor (column -> JSON path within a conforming `data` payload), and writability.

   **Writability is the last step, and is skipped entirely for read-only queries** — it only runs when the query is actually a write.

   A physical column is writable iff it's referenced exactly once in `select`, wrapped only by coalescing operators (`??`, `||?`, `coalesce`) or by `set`/`get-set`. `get` doesn't count toward this at all — it's read-only, excluded from write-side accounting entirely. `set`/`get-set` references share the *same* occurrence bucket as bare references to that column ; a column supplies its write value from exactly one place, full stop — that's the existing exactly-once rule, not a separate allowance for `set`/`get-set`.

   A relation's rows are writable only if its identity-target columns (PK, or `on_conflict`'s columns) come out writable by that rule. Non-writability on a child can force the whole query read-only unless that child was explicitly set readonly — a tree-level propagation over the per-node results, not decidable from any one node in isolation.

## Scope / self-reference

A node's scope is its own relation's columns plus its visible children's join aliases (`OuterAlias`, per `OutgoingNodes`/`IncomingNodes`) — never its parent's, never a sibling's (sibling visibility is explicitly excluded for v1, despite `query.ts`'s alias comment mentioning siblings).

A node's own declared alias (`InnerName`) resolves within its own expressions too, self-referencing its own columns — not only usable by children.

> Why: even where redundant, self-qualification (`self_alias.column`) gives generated SQL an explicit, always-correct way to name a column, rather than relying on bare/unqualified names and real Postgres correlated-subquery scoping rules (inner scope shadows outer) to resolve correctly on their own.

## Type checking

No value-type checking (text/int/numeric/...) — Postgres already does that at prepare time, and duplicating it here would be large, error-prone, and redundant. Only *kind* checking (leaf vs. composite vs. embedded join vs. inline object literal) is needed, for dot-chain navigation — `ResolvedField` already models this. This applies to every path operator that chains through a value, not just `.` : `->`, `->>`, `#>`, `#>>` all resolve their later hops the same way.

## Cycle detection : not needed

Shape resolution doesn't need cycle detection or a "currently visiting" set. With sibling visibility excluded, a dot-chain can only travel downward into a node's own already-parsed children — a finite JSON-derived tree, not a graph, so no cycle is constructible. Memoization is still worth keeping (a shape can legitimately be requested from more than one place), but purely as a performance nicety now, not for correctness.

## Identifier resolution

Resolving an `Identifier` (or a later hop in a `.`/`->`/`->>`/`#>`/`#>>` chain) is the same operation as deriving `ResolvedField` above, not a separate mechanism — resolving a name *is* producing (or looking up) a `ResolvedField`. What it can produce differs by position in the chain :

- **First hop** (resolved against the current node's Scope) : a physical column of the current relation ; the node's own alias (`InnerName`), self-referencing its own columns ; a child join alias (`OuterAlias`), the start of an embed reference ; or unresolvable — a hard error.
- **Later hops** (resolved against whatever the previous hop landed on, not against Scope) : a sub-field of a composite-typed column ; a further child join/embed, if the previous hop was itself one ; a key of an inline object literal in that node's `select` ; or a terminal scalar, valid only as the last hop.

Not part of this : `$param`'s name. It already has its own AST node (`ParamExpr`) and resolves against a well-known query's declared param list, never through Scope — keep that boundary, don't fold it into identifier resolution later.

`ResolvedField` itself needs (at least) three concrete variants to cover the above : a column-backed one wrapping `*pg.Column` (covers both a plain relation column and a composite sub-field — both are ultimately "this name is this physical column," just sourced from a different `ColumnsMap`), a node-backed one wrapping `*QueryNode` (an embed), and a literal-backed one wrapping a nested inline object literal's own field map. No separate "scalar leaf" variant is needed — that's the column-backed variant with nothing further to chain into ; attempting to chain past it is what produces the error, not a distinct type.

Two Go-level domains do the actual resolving, and they're not the same mechanism :

- **Scope-domain** : `Identifier` nodes (any hop position) plus three narrower cases that may *only* land on a plain physical column, never an alias or embed — `GetExpr.Column`, `SetExpr.Column`, `GetSetExpr.Column`, and the `[]string` `Except` lists (`OwnExceptExpr`, `FullExceptExpr`, `OwnExceptAndExpr`, `FullExceptAndExpr`). These are plain Go `string`/`[]string` fields, not `Identifier` nodes, so they need their own resolution path even though the underlying lookup (Scope -> column) is the same as `Identifier`'s.
- **Catalog-domain** : `AggExpr.Identifier`/`CallExpr.Identifier`, typed `FunctionRef` (`query/expression.go`) — resolved against `pg`'s function catalog via search path, blacklist-checked against `config.Blacklist`, with no relationship to a `QueryNode`'s Scope at all. `FunctionRef` is parsed from either a bare string (an unqualified name, resolved via search path — never split on `.`, since a quoted Postgres identifier can itself contain a literal dot) or an explicit `{schema, name}` object ; see `FunctionRef`'s doc comment for the full reasoning.

Not identifier resolution, despite looking similar : the keys of `ObjectExpr.Fields`/`OwnAndExpr.And`/etc. are output field names the query author is choosing, not references to anything, so there's no lookup to perform on them.

## Not yet wired

- `query.maxdepth` (`config.Query.MaxDepth`) : no enforcement exists yet. Pure pass-1, tree-depth concern — unrelated to expression resolution, don't conflate the two.
