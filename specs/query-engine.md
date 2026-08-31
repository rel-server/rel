# Query Engine

Rel's most important feature is its querying capabilities.

Similarly to GraphQL and PostgresT, it offers a complex query engine able to span across relations in the database to produce intricated and complex content.

Unlike GraphQL, there are no "mutations" to describe ; unlike PostgresT, the complex form that it generates can be sent back as-is to the server so that it updates accordingly ; it makes inserting or updating rows of linked tables in a single transaction possible, even and especially when related rows depend on a identifying key not yet known (ids) because the parent doesn't already exist.

Selecting is based on a relation. Foreign keys allow embedding of a distant resource into the result : whether from the table to another or in reverse. When embedding a remote relation that has multiple rows to the current one, embeds an array. Otherwise, stays as a simple object.

Compiling a query JSON tree into SQL happens in two passes, both implemented and tested :

- **Pass 1 — tree inflation + DB resolution** (`query/node_parse.go`'s decode step, `query/node_resolve.go`'s resolve step, plus `pg/info_searchpath.go` and `pg/info_lookup.go` for the search-path/by-name lookups it needed that `pg` didn't have yet — see those files' tests for coverage). Builds the `QueryNode` tree from JSON and resolves every DB-facing reference against `pg` introspection : `relation`/`function`+`schema`/`arguments` -> `*pg.Relation`/`*pg.Function` (`relation` and `function` are mutually exclusive — exactly one is required, and `arguments` is only legal alongside `function`), `on` -> `ResolveJoin`, `on_conflict` -> a real constraint. The relation/function-as-root blacklist check belongs here, at the point each node's own relation/function is resolved — not in pass 2. Expression-typed fields (`where`, `select`, `order_by`, `distinct_on`, function `arguments`) are parsed (`query/expression_parse.go`) but not resolved during pass 1 — bare strings stay opaque `Identifier` nodes.

  > Why: a node's own relation/function is resolved against `pg` the same way `on`/`on_conflict` are, not against an `Expression` tree, so its blacklist check naturally sits next to that resolution rather than being duplicated into expression walking.

- **Pass 2 — expression resolution** (`query/expression_resolve.go`'s resolution step, `query/shape.go`'s shape/writability step, `query/scope.go`'s `LookupInScope` — see those files' tests, plus `query/expression_resolve_test.go`, for coverage). Two steps, not one : **Resolution**, a generic walk (`ResolveExpressions`) binding every bare identifier to a column/alias/param and blacklist-checking every `call`/`agg` identifier — see `## Scoping` for the full mechanics ; and **Shape/writability derivation** (`DeriveShapes`) — see `## Writability`.

## Search path

Roles we switch to when requests are made are NOT respected, because that would mean having to query them every time. The only enforced search path will be the one of the base user we connect the database to.

## Configuration

* `pg.uri` : a full `postgres://user:pass@host:port/db` connection string. When set, authoritative — the granular fields below are ignored entirely, not merged with it. The simplest possible setup is `pg.uri` alone.
* `pg.user` / `pg.password` / `pg.host` / `pg.port` / `pg.database` : the primary Postgres connection, used when `pg.uri` is unset. This is the login rel uses to connect to the database to perform migrations with dmut, to introspect the database at startup, and — unless `pg.query.user` overrides it — to serve requests, i.e. the role from which `set role` to all other roles is executed.
* `pg.query.user` / `pg.query.password` (default : `pg.user` / `pg.password` if provided) : an OPTIONAL, narrower-scoped login for the connection that actually serves requests specifically. Documented and encouraged for a hardened deployment, never required — `set role` per request, not this login's own privileges, is what actually restricts what a request can access ; introspection and dmut migrations always use the primary connection above, never this one.
* `pg.query.anonymous_role` (default `~anonymous`) : the role rel switches to for requests without credentials of their own. Full lifecycle (when/how this applies, alongside JWT verification) is `authentication.md`'s `# Roles` concern — this entry exists here only because it's also part of `## Configuration`'s connection-role settings.
* `pg.pool_size` (default `10`) : the max number of connections in the pool that serves requests. Never affects startup introspection or dmut migrations, which each use one short-lived connection regardless of this setting.

* `pg.query.max_depth` (default `6`) : maximum depth a query can specify

Unlike route functions, all queries are sent on `/rel`, and all of them MUST be `POST`.

When querying resources they're not allowed to access in the database, the status will be `401`. A unknown relation will result in `400`, as `/rel` will never be 404 itself.

`/rel` only returns JSON, even when it replies an error.

Group by and window functions are intentionally disabled ; these should be done inside views instead.

> Why: too much abuse potential, and group by anyway disables writing back entirely on the relation. Rel is about selecting data to write it back (mostly,) the rest can be done in views.

To avoid paying for parsing and preparing statements all the time, rel offers a "well-known" queries mechanism that are read on server startup or after reloading a schema. They are named and make use of the `["$param", ...]` expresion which are transformed into prepared statement param. Another advantage of well-known queries is that they're also exported by rel's typescript/javascript export and typed appropriately. See `specs/well-known-queries.md` for the full mechanism.

## Implementation

* Use github.com/bytedance/sonic
* Do not use struct tags ; JSON must be parsed using .Get and other iterative methods for performance
* Send JSON to postgres, (ab)use json(b)_populate_record and json(b)_populate_recordset
* Use COPY to send the JSON to TEMP tables in postgres

## Transactions

A request is one transaction, full stop — a query or several queries (a `Sequence`, in one
HTTP request) share a single `begin`/`commit`, covering the write phase AND every item's own
read-back alike, not just the writes. Any error, in a write OR a read, rolls back the whole
thing.

> Why a request is one transaction, not just its writes : a read can call into a Postgres
> function, and a function can raise. If a read "going awry" after a write has already
> happened left that write committed, the request as a whole would have partially succeeded
> in a way the client has no clean way to detect or recover from — treating the whole request
> as one atomic unit is simpler to reason about, and consistency is worth it even though it
> isn't free (see the accepted costs below). This applies uniformly, including a `Sequence`
> made entirely of plain reads (no write item at all) — every item in it now shares one
> snapshot, not just writes-vs-writes.

**Implementation note (`server/rel.go`)** : `begin` runs before Lifecycle step 5 (Apply
role — `SET LOCAL ROLE`, transaction-scoped, so it applies for the whole request and reverts
automatically at the single commit/rollback below, no separate reset-role cleanup needed).
Every write item's phase 1/2 runs, then every item's response statement streams — write
items reading back what they just wrote, read items running their own query — all still
inside that one open transaction. `commit` is the very last thing the handler does, only
once every item (write and read alike) has fully succeeded ; any failure along the way,
write or read, rolls back explicitly rather than relying only on the connection pool
discarding a non-idle connection on release.

**Accepted cost, not an oversight.** Because the read-back streams manually (`## Response
Shape` — no `json_agg`, to keep memory roughly constant even for a large result), the
transaction stays open, and its locks/snapshot stay held, for as long as the client takes to
receive the full response, not just for the write itself. A slow or throttled client reading
a large result therefore holds real Postgres locks (from the write phase) and a live snapshot
open for that whole duration — on a busy table this can contend with other transactions
touching the same rows, and a long-held snapshot also defers vacuum from reclaiming dead
tuples. There is a second, more specific cost this ordering creates : commit only happens
*after* streaming succeeds, so a commit failure at that exact point happens after the
response may already be fully sent to the client. For a multi-item `Sequence`, the response
is at least left truncated (missing its closing `]`) — genuinely invalid JSON, a real (if
blunt) failure signal to any real parser. For a **single-item** request there is no such cue
at all : its entire response can already look completely valid by the time a late commit
failure happens, so a client can in principle observe what reads as a successful response for
a write that was then rolled back. Avoiding this outright would mean buffering the whole
response before writing anything, which is exactly the memory tradeoff `## Response Shape`
already rejected — so this is named here as a known, accepted limitation, the same treatment
`rpc.md`'s own "no true streaming to Postgres" note already sets precedent for, not a promise
this document is pretending to keep.

## Scoping

Proper scoping is to be enforced when walking the json query tree ; it is an error to refer to unknown columns or relations, and this MUST be caught by the "compiler". Aliases must be correctly propagated in the right scopes ; subqueries and parent queries do not see the same identifiers.

The scope will handle look-up for ; relations, functions, but also local relation aliases and columns, regular and computed.

Functions may be blacklisted for use in the query through config : `blacklist.functions.<schema>.<function_name_or_operator or *>` with `y` or `true` to effectively disable them for use in the query builder. Rel must be aware of the search path when inspecting functions being called.

Similarly, relations may be blacklisted for the same reason. Default blacklist :
- `blacklist.relations.pg_catalog.*` : `y`
- `blacklist.relations.information_schema.*` : `y`

Default functions blacklist :
- `blacklist.functions.pg_catalog.set_config` : `y`
- `blacklist.functions.pg_catalog.pg_sleep` : `y`
- `blacklist.functions.pg_catalog.pg_terminate_backend` : `y`
- `blacklist.functions.pg_catalog.pg_cancel_backend` : `y`
- `blacklist.functions.pg_catalog.pg_advisory_lock` : `y` (and the rest of the `pg_advisory_*lock*` family, minus the `_unlock` variants, which are harmless) — `PUBLIC`-executable by default, and holding a session/transaction advisory lock indefinitely is a cheap way to wedge a connection or contend with any advisory locks rel's own runtime might use internally.

> Why these two and why wildcarded : this is what closes the open question raised in an earlier draft of this section — both schemas are readable by `PUBLIC` by default (`pg_settings`, `pg_stat_activity`, `information_schema.tables`, ...) and reachable through the ordinary `relation`/`schema` fields on a query, same as any table. Wildcarding the whole schema rather than naming individual views is deliberate here, unlike the function blacklist above : Postgres ships and changes the exact set of catalog/information_schema views across versions, so pinning specific names would need to be kept in sync with every version rel supports, whereas "nothing in these two schemas is a valid query target" is a version-independent rule that never needs updating. A user who genuinely wants to query one of these (introspection tooling, say) can still override the specific entry back to `n`.

The database role rel connects with to the server in order to perform requests should never be `postgres` or superuser, and should never be a member of `pg_read_server_files`, `pg_write_server_files`, `pg_execute_server_program`, or `pg_signal_backend` - a stark warning must be printed if this is the case. The developer must be incited to create a role of some kind that will receive grants for all subroles that shall exist within the database and give it to `pg.query.user`.

> Why this still matters alongside the blacklist : the blacklist can only stop what it already knows the name of. It's a maintained list, not a closed one — a newly `CREATE EXTENSION`'d function (which defaults to `PUBLIC EXECUTE` the moment it's created, e.g. `dblink`, `postgres_fdw`) isn't covered until someone notices and adds it. The role restrictions above are the backstop for exactly that gap : as long as the role never holds those privileges/memberships, most of what makes a *newly discovered* dangerous function actually dangerous (arbitrary file/network/process access) stays unreachable regardless of whether the blacklist has caught up yet.

### Self-reference and child scope

A node's scope is its own relation's columns plus its visible children's join aliases (`OuterAlias`, per `OutgoingNodes`/`IncomingNodes`) — never its parent's, never a sibling's (sibling visibility is explicitly excluded for v1, despite `query.ts`'s alias comment mentioning siblings).

A node's own declared alias (`InnerName`) resolves within its own expressions too, self-referencing its own columns — not only usable by children.

> Why: even where redundant, self-qualification (`self_alias.column`) gives generated SQL an explicit, always-correct way to name a column, rather than relying on bare/unqualified names and real Postgres correlated-subquery scoping rules (inner scope shadows outer) to resolve correctly on their own.

### Type checking

No value-type checking (text/int/numeric/...) — Postgres already does that at prepare time, and duplicating it here would be large, error-prone, and redundant. Only *kind* checking (leaf vs. composite vs. embedded join vs. inline object literal) is needed, for dot-chain navigation — `ResolvedField` already models this. This applies to every path operator that chains through a value, not just `.` : `->`, `->>`, `#>`, `#>>` all resolve their later hops the same way.

### Cycle detection : not needed

Shape resolution doesn't need cycle detection or a "currently visiting" set. With sibling visibility excluded, a dot-chain can only travel downward into a node's own already-parsed children — a finite JSON-derived tree, not a graph, so no cycle is constructible. Memoization is still worth keeping (a shape can legitimately be requested from more than one place), but purely as a performance nicety now, not for correctness.

### Identifier resolution

Resolving an `Identifier` (or a later hop in a `.`/`->`/`->>`/`#>`/`#>>` chain) is the same operation as deriving a `ResolvedField` — resolving a name *is* producing (or looking up) a `ResolvedField`. What it can produce differs by position in the chain :

- **First hop** (resolved against the current node's Scope) : a physical column of the current relation ; the node's own alias (`InnerName`), self-referencing its own columns ; a child join alias (`OuterAlias`), the start of an embed reference ; or unresolvable — a hard error.
- **Later hops** (resolved against whatever the previous hop landed on, not against Scope) : a sub-field of a composite-typed column ; a further child join/embed, if the previous hop was itself one ; a key of an inline object literal in that node's `select` ; or a terminal scalar, valid only as the last hop.

A bare name colliding across more than one thing in scope (a column, a child's join alias, and/or this node's own alias) is a **hard error** (`LookupInScope`), not silently resolved by precedence — picking one on a collision would let a query run and silently return data other than what the author meant.

A function-position node's own call `arguments` resolve against its **parent**'s scope (correlating to the enclosing query, same as any subquery's arguments would) ; a root-level function call (no parent) can only use literals/params.

Not part of this : `$param`'s name. It already has its own AST node (`ParamExpr`) and resolves against a well-known query's declared param list, never through Scope — keep that boundary, don't fold it into identifier resolution later.

`ResolvedField` has three concrete variants (`query/resolved_field.go`) : `ColumnPath{Node, Path, ElementType}` (a plain relation column, or a composite sub-field — both are ultimately "this name is this physical column," just sourced from a different `ColumnsMap` ; `Node`+`Path` together, not the terminal `*pg.Column` alone, are the identity — two columns sharing a composite type yield the same terminal pointer once navigated into, which would otherwise collide ; `ElementType` overrides the type used for the *next* compositeness check, set only right after an `["index", ...]` hop unwraps one level of array), `*QueryNode` (an embed, self-reference included), and `Shape map[string]ResolvedField` (the landing of *any* select-shape-producing expression — `own`/`full` and their `-except`/`-and` variants, or an inline object literal — uniformly, at any nesting depth).

**There is no special case anywhere in the resolver for "a node's own top-level `select`" versus "a shape-producing expression nested arbitrarily deep inside another one."** Both go through the exact same recursive mechanism (`resolveChain`'s cases for `OwnExpr`/`FullExpr`/`OwnExceptExpr`/`FullExceptExpr`/`OwnAndExpr`/`FullAndExpr`/`OwnExceptAndExpr`/`FullExceptAndExpr`/`ObjectExpr`, all delegating to the shared `buildShape` helper, `query/expression_resolve.go`). An `own-and` three levels inside an object literal builds its `Shape` and is chained into exactly the same way `own-and` used as a node's whole `select` is — this was an explicit correction : an earlier version of this design special-cased "top-level select" via a narrower `deriveComputedFields`, and that distinction turned out to have no principled justification (own/full-and's base-column-plus-computed-key shape is the same *kind* of thing regardless of where in the tree it appears), so it was removed in favor of one recursive mechanism.

**A hop into an embedded `*QueryNode` reaches both physical columns/aliases (`LookupInScope`) and its exported `Shape` (`resolveExternalHop`, `query/expression_resolve.go`).** A name matching both is only a hard error if the two sources actually *disagree* on what it means (`resolvedFieldsEqual`) — agreement (e.g. an ordinary unrenamed column, legitimately found both via `LookupInScope` and via own/full's mirroring of that same column into `Shape`) is fine, not a false-positive collision. The `Shape` half is only legal for an *external* hop (from a parent) — a node's own `where`/`select`/`distinct_on`/`order_by` must not see that same node's own `Shape` (enforced by `ResolveContext.resolvingOwn`, set for that node's own-expression-resolution block regardless of which of those fields the self-hop originates from, since `where` resolves before `select` and must be blocked identically), matching the already-established "no forward-reference within one select object, no sibling access" rule. `resolveExternalHop` computes (and memoizes onto `target.Shape.Fields`, via `selectShape`) this on demand ; sound because resolution is strictly bottom-up, so a target's own `select` is always fully resolved before anything external can hop into it — no chicken-and-egg with the later, separate `DeriveShapes` pass, which reuses/completes the same cache (`selectShape`) rather than fighting it. `ResolveContext.shapeInProgress` is a second, narrower guard specifically on `selectShape`'s own reentrancy (a self-hop reached *from inside* the `select` being built, e.g. one computed key referencing another via a self-alias) — a backstop against unbounded recursion, kept even though `resolvingOwn` already rules out the same scenario at the `resolveExternalHop` level.

One consequence worth naming explicitly : an `own-except-and`/`full-except-and` key that overrides an *omitted* column under that column's own name (legal per `query.ts`'s "merge_with can specify keys that were omitted ; they shall override it") is thereby unreferenceable from a parent's `where`/`order_by` under that same name — `LookupInScope` on the child always finds the real physical column regardless of what `select` exports it as, so an external hop by that name is genuinely ambiguous between "the real column" and "the overridden export value," and correctly hard-errors rather than picking one.

`ObjectExpr` and `["index", ...]` are also chainable : a *nested* inline object literal (a literal used as the value of another key) produces a `Shape` landing instead of being silently opaque ; an array-index hop (`["index", "col", 1]`) unwraps one level of array before a further `.` hop, via `ColumnPath.ElementType`.

`GetExpr`/`GetSetExpr` land directly on a `ColumnPath` (their own single column), not wrapped in a `Shape` — they reference one value, not a keyed object, so there's nothing to key into. A node whose top-level `select` is a bare `get`/`get-set` therefore does not synthesize a one-entry `Shape` named after its column — a parent hopping into such a child by that same column name already finds it via `LookupInScope` (the column is real and unrenamed) without needing `Shape` to duplicate it.

Two Go-level domains do the actual resolving, and they're not the same mechanism :

- **Scope-domain** : `Identifier` nodes (any hop position) plus three narrower cases that may *only* land on a plain physical column, never an alias or embed — `GetExpr.Column`, `SetExpr.Column`, `GetSetExpr.Column`, and the `[]string` `Except` lists (`OwnExceptExpr`, `FullExceptExpr`, `OwnExceptAndExpr`, `FullExceptAndExpr`). These are plain Go `string`/`[]string` fields, not `Identifier` nodes, so they need their own resolution path even though the underlying lookup (Scope -> column) is the same as `Identifier`'s.
- **Catalog-domain** : `AggExpr.Identifier`/`CallExpr.Identifier`, typed `FunctionRef` (`query/expression.go`) — resolved against `pg`'s function catalog via search path, blacklist-checked against `config.Blacklist`, with no relationship to a `QueryNode`'s Scope at all. `FunctionRef` is parsed from either a bare string (an unqualified name, resolved via search path — never split on `.`, since a quoted Postgres identifier can itself contain a literal dot) or an explicit `{schema, name}` object ; see `FunctionRef`'s doc comment for the full reasoning.

Not identifier resolution, despite looking similar : the keys of `ObjectExpr.Fields`/`OwnAndExpr.And`/etc. are output field names the query author is choosing, not references to anything, so there's no lookup to perform on them.

### Composite-type introspection

Composite-column chaining (the `Identifier resolution` section above) depends on `pg.Type.IsComposite()`/`.Relation` resolving correctly, which needed two `pg` package fixes :

`INFO_QUERY_RELATIONS` (`pg/info_relation.go`) sourced its columns entirely from `information_schema.columns`, whose own view definition filters to `relkind IN ('r','v','m','f','p')` — a bare `CREATE TYPE ... AS (...)` composite type (`relkind = 'c'`) was never introspected at all, so `pg.Type.IsComposite()`/`.Relation` silently reported `false`/`nil` for exactly the case `ResolvedField`'s composite-navigation design depends on. Fixed by unioning in a second branch sourced directly from `pg_attribute` for `relkind = 'c'` relations — no overlap with the existing branch, since a table's own row type lives on the table's `pg_class` row (`relkind = 'r'`), never as a separate `'c'` entry.

Separately : a domain over a composite type (`CREATE DOMAIN d AS some_composite_t`) has no `typrelid` of its own (only the base type does), so `IsComposite()` checked directly on a domain reports `false` even one hop from being composite. This turned out to *not* matter for a domain-typed table column — `information_schema.columns`' own `udt_name` already reports the base type directly for those, bypassing the domain layer before it ever reaches `pg.Column.Type` — but it does matter for a domain-typed *field of a composite type*, introspected via the `pg_attribute`-based branch above, which reads `atttypid` directly and does not auto-unwrap. Fixed with `pg.Type.Underlying()`/`CompositeRelation()`, which unwrap through any domain chain ; `ColumnPath`'s composite check now goes through `CompositeRelation()` rather than `IsComposite()`/`.Relation` directly.

### Deferred validation and security

Whether deferring `.`/jsonb navigation validation to Postgres (rather than resolving it eagerly at compile time) is a security concern : no. Rel's safety property (every SQL identifier checked against real `pg` introspection, every value `$N`-bound) holds regardless of *when* `.`/`->` resolution happens — the real question is never "syntax injection," it's "can deferred resolution let someone reach something the blacklist should have stopped." Traced case by case : jsonb navigation can't escape a value already read from an already-blacklist-checked column (an unknown key just returns NULL, doesn't even error) ; composite sub-fields aren't independently blacklistable objects, just names within an already-exposed type ; hopping into a computed `select` key only re-references an expression the *same* client already fully controlled earlier in the *same* query ; hopping into an embed at all was already gated once, when the embed was declared in `join` (pass 1's relation blacklist). No bypass path in any of these.

Where "defer to Postgres" *would* have real teeth is proxying raw Postgres error text back to a client (schema-enumeration via error fingerprinting) — but that's a general error-sanitization gap independent of this decision, already tracked in `specs/TODO.md`'s `## Errors` entry (no taxonomy yet).

The actual reason compile-time resolution is still needed is **not security** : for writes, the extractor's `JsonPath` has to be known before a request ever reaches Postgres (Postgres has zero visibility into rel's `data` JSON shape, nothing for it to validate there) ; for reads, codegen still needs to know *which* SQL construct to emit (composite access vs. correlated subquery vs. jsonb operator) rather than one generic form. Both hold regardless of the security question.

### Join eligibility : indexing, not just correctness

A join's `on` mapping (see `query.ts`) must be backed by a foreign key constraint, or — for a non-FK join — by a unique constraint on whichever side is the "one" side. Either way, the **child's own `on` columns** (the relation being described, per `query.ts` — i.e. whichever side isn't the enclosing/parent query) MUST be covered by an index on those exact columns, or rel refuses to compile the query. This is a hard, unconditional compile-time error, with no config escape hatch — consistent with the rest of this section defaulting to strict (mandatory `on`, no ambiguity, blacklist-by-default).

> Why : the Reading Algorithm (`## Reading Algorithm`) runs a correlated subquery per node, executed once per parent row — always scanning the *child* relation, filtered by its own `on` columns, regardless of which side ends up being the "one" or the "many" side of the resulting embed. An earlier draft of this rule said "whichever side is the many side," which is usually right but isn't quite precise : the child side is what actually gets scanned either way, and when the child side is unique that scan is already indexed for free (a unique constraint always creates its own supporting index) — so stating the rule as "the child's columns, always" subsumes the many-side case rather than being a separate rule from it. Without an index backing them, that's a sequential scan per parent row — silently, since nothing about the query *looks* wrong, it's just a performance cliff waiting for the table to grow. This is not only a non-FK-join concern : Postgres does **not** automatically index the referencing side of a foreign key (only the referenced/unique side is guaranteed an index, because the constraint requires one). `customers → orders` via `orders.customer_id` is exactly as capable of degrading to a per-parent-row seq scan as any ad-hoc join would be if nobody thought to add `CREATE INDEX ON orders(customer_id)`. So this check applies uniformly, FK-backed or not — it is not a special case bolted onto the non-FK path.

What counts as "covered by an index," precisely, given a set of columns to check against a relation's indexes :

- Every key in `on` becomes an equality predicate, so a btree index serves it regardless of the order those columns were declared in — the check is whether the candidate columns, as a *set*, equal the *leading key columns* of some index on that relation, also taken as a set. Order within that prefix doesn't matter ; only that every one of them is a genuine leading key column.
- Only the index's first `indnkeyatts` columns count. An `INCLUDE`d column (Postgres 11+ covering indexes) sits in `indkey` past that boundary and is not usable for the lookup itself, only for avoiding a heap fetch — verified directly : `CREATE INDEX ... (customer_id) INCLUDE (total)` gives `indkey = "2 3"` but `indnkeyatts = 1` ; only `customer_id` counts.
- A **partial** index (`indpred IS NOT NULL`) does not count. It only guarantees coverage for rows matching its predicate, which rel has no way to verify subsumes the query's actual row set at compile time.
- An **expression** index does not count for this check ; `indkey` carries a `0` at any expression position (verified directly), and `on` only ever joins on plain columns, never on the result of an expression.
- A unique constraint's supporting index already satisfies this for whichever side is unique — no separate index check is needed on that side, only on the many side.

This requires introspecting `pg_index` itself (`indkey`, `indnkeyatts`, `indisunique`, `indpred`, `indexprs`) as its own capability, independent of named constraints — a table's index inventory and its constraint inventory are related but distinct facts, and rel needs both.

rel doesn't treat foreign keys as special to eligibility — the actual rule is "unique value, indexed access," and a foreign key is just one common way that's satisfied, via the unique constraint its target is required to have. But when a real FK *does* exist between two relations over exactly the column set an `on` mapping supplies, and the pairing doesn't match that FK's declared correspondence, rel rejects it rather than falling through to the generic set-only unique+indexed check. Not because the FK makes it structurally invalid — SQL-wise it's a perfectly legal join — but because reusing precisely the columns a real FK already claims, paired differently, is near-certainly a swapped/typo'd `on` rather than a deliberate second relationship. A genuinely distinct relationship using different columns is unaffected by this ; it only fires when the column sets coincide exactly with an existing FK's.

## Writability

For a query to be bidirectional, there is a notion of writability of a column ; a column is said to be writable if and only if it appears exactly once in the select expression and is not transformed by anything other than coalescing operators. Columns are tracked and are writable even if they appear in sub-objects.

A relation's rows are writable iff the columns of its identity target (primary key by default, or whatever on_conflict explicitly designates as the conflict-resolution unique constraint) are present and writable exactly *once* in the select output.

If a child query disables writability for its own table, it disables it for the whole query, unless it was *explicitely* set to readonly. A user attempting a write on such a query receives an error indicating the offending relation.

`distinct` and `distinct_on` do not need to disable writability on their own ; the general contract two paragraphs up (identity target present and writable exactly once) already covers the only case that would actually be dangerous.

> Why : you're not confused, this is right. Plain `distinct` deduplicates on the *entire* projected row. If the identity target is part of that row, two different underlying source rows can never produce equal tuples in the first place — the identity columns alone already guarantee tuple inequality between them — so `distinct` can never actually merge two source rows together when identity is present ; it's a no-op with respect to row correspondence. It only becomes dangerous when identity is *absent* from the select (e.g. `select distinct city`, where one output row could stand for many different users) — and that case is already excluded by the general rule regardless of `distinct`. `distinct_on` reasons the same way : it doesn't merge rows either, it picks exactly one real row per group (via `order_by`), so the identity columns on that output row still correctly name the one real row it came from.

**Derivation** (`DeriveShapes`, `query/shape.go`) runs bottom-up, over the already-resolved `select` tree only — `where`/`order_by`/`distinct_on`/etc. don't produce an exported shape or writable columns, so this step doesn't touch them. Produces, per node (`query/node.go`'s `QueryNode.Shape`, a `*NodeShape`) : the exported shape (`query/resolved_field.go`'s `ResolvedField`), the extractor (column -> JSON path within a conforming `data` payload), and writability. **Writability is the last step, and is skipped entirely for read-only queries** — it only runs when the query is actually a write.

A physical column is writable iff it's referenced exactly once in `select`, wrapped only by coalescing operators (`??`, `||?`, `coalesce`) or by `set`/`get-set`. `get` doesn't count toward this at all — it's read-only, excluded from write-side accounting entirely. `set`/`get-set` references share the *same* occurrence bucket as bare references to that column ; a column supplies its write value from exactly one place, full stop — that's the exactly-once rule, not a separate allowance for `set`/`get-set`.

Composite sub-fields are independently writable (Postgres allows `UPDATE t SET comp.field = ...`, and — verified directly against Postgres 16 — the same dotted form as an INSERT column-list target : `INSERT INTO t (comp.field) VALUES (...)`) — occurrence-counting and the extractor key on the *full* `ColumnPath.Path`, not just the containing column, so `home` alone, `home.city`, and `work.city` (two columns sharing the same composite type) all track as distinct write targets, never colliding on the shared terminal `*pg.Column` pointer.

**Execution, not just derivation** (`write_dml.go`, `write_denormalize.go`) : a composite sub-field write is a real INSERT/UPDATE/UPSERT column-list target (`writeTargetPath`, `write_dml.go`), never a synthesized `ROW(...)` construction — Postgres itself fills every other field of the composite as `NULL` on an INSERT into a previously-absent/`NULL` composite, and leaves them untouched on an UPDATE (both verified directly). The value itself is carried through `_data.data` under a synthetic, `__`-joined flat key (`columnPathFlatName`, e.g. `home__city` — matching this codebase's own `__row_id`/`__node_id`/`__parent_id` convention for internal, never-a-real-column names) rather than as a nested JSON object populated through `jsonb_populate_record`'s own composite-reconstruction — deliberately : `jsonb_populate_record` only helps when every field of a nested object is meant to compose one call to it, whereas each composite sub-field here is independently tracked, extracted, and cast to its own leaf type, the same way a plain column already is. An UPSERT's `ON CONFLICT DO UPDATE SET`, reading the proposed row off `excluded`, needs the row-value-parenthesized form (`(excluded.home).city`, not `excluded.home.city` — the latter is a syntax error, parsed as a table reference, verified directly) — `WriteQualifiedPath` (`resolved_field.go`) is shared with the read side's own equivalent (`## Reading Algorithm ### Scalar hop through a to-one relation`) for exactly this parenthesization.

> Selecting a composite column **both** whole (`own`/`full`, or bare) **and** one of its own sub-fields independently in the same write is not specially rejected by this package — Postgres itself refuses it (`"column ... specified more than once"` / `"multiple assignments to same column"`), since the two write targets genuinely conflict at the SQL level despite being tracked as independent, individually-legitimate targets by the rule above. Left to that native rejection rather than duplicated as an earlier check.

A bare composite `.` chain used directly as a select value (e.g. `{"c": [".", "home", "city"]}`) counts as **one** clean reference to the terminal sub-field, same as a bare column reference — not two (the containing column plus the sub-field), which would make it permanently unwritable regardless of duplication. A `.` chain that hops **into a child** (e.g. `{"t": [".", "movies", "title"]}`) is never a write target of the node doing the selecting — that column belongs to the child's own, separately-derived Shape, not smuggled into the parent's.

> Question: a `.` chain that hops through an `["index", ...]` anywhere along the way is conservatively excluded from writability entirely, even as an otherwise-clean single reference — `ColumnPath`'s identity key carries no record of *which* array element was navigated through, so two different indices would collapse onto the same key with no way for a write-side extractor to know which element a value belongs to. Whether an indexed array element should be a legal write target at all is still open, deferred to whoever designs the write extractor for that case rather than defaulted into silently here.

`own`/`full` (and their `-except`/`-and` variants) enumerate physical columns only, sourced from `pg_attribute` at introspection time — they never implicitly pull in a computed column (a function taking the relation's row type as its argument, callable via `alias.func_name` or `func_name(alias)` in Postgres — see `## Scoping ### Identifier resolution` and `## Reading Algorithm ### Function-rooted nodes` for how a self-alias reaches this). A computed column is only included when named explicitly in `select`, at which point it resolves through the same function-identifier path — and is subject to the same `## Scoping` rules — as any other `["call", ...]`. It is never a candidate for writability, since it isn't a real column to begin with. Expression columns are not write candidates for the same reason.

## Reading Algorithm

Much simpler than writing : no phases, no `_data` temp table, no dependency ordering — one recursive walk of the query tree that emits a single correlated `SELECT`, per node.

### Function-rooted nodes

A function-rooted node also gets `Relation` populated — via `GetRelationByType` on its return type, when that resolves to a known composite/relation (an ordinary composite return, or `SETOF <relation>`) ; when it doesn't — a `RETURNS TABLE(...)`/OUT-parameter function, whose `prorettype` is always the single generic `pg_catalog.record` pseudo-type with no backing composite type for `GetRelationByType` to resolve — falling back to `fn.RecordRelation`, a synthetic `*Relation` built at introspection time directly from the function's own OUT/TABLE-mode arguments (`pg.Function.RecordRelation`, `specs/introspection.md ### Functions`) ; still nil for a function with no OUT arguments at all, i.e. a bare scalar return. A table-valued function is joinable exactly like the type it returns — `QueryNode`'s "either Relation or Function" doc comment states this directly : `Function != nil` decides *kind*, `Relation` is what descendants/joins resolve against, and a function node legitimately has both set. It is NEVER writable, though — see the paragraph below.

> A `RETURNS TABLE` function's `RecordRelation` is structurally barred from ever being eligible as the CHILD/joined-into side of any relationship — Postgres can't index a function's computed output, and `## Scoping ### Join eligibility` requires exactly that on the child side — only a query root or the parent/outer side of an outgoing join out to a real indexed relation.

**A function-rooted (or function-embedded) node is unconditionally UNWRITABLE, regardless of
`write_mode` or whether its `Relation` resolves to a real, otherwise-writable table.** The
Writing Algorithm always targets `Relation`'s own underlying table directly (`insert into
target_relation ...`, by name), never "through" the function that was used to read it — so
any filtering a function's own SQL body does (a `where owner_id = ...`, a soft-delete filter,
anything at all) is silently bypassed for writes. Concretely : a function defined as `select
* from director where public = true` only ever shows public directors on read, but if writes
were allowed through that same node, a client could upsert a row by `id` the function itself
would never have exposed to them, since the write path never consults the function's own
`where` at all. A Postgres VIEW has a real, enforced guard against exactly this : it can only
ever be written through if Postgres itself considers it auto-updatable, or it has an `INSTEAD
OF` trigger — either way, the view's own defining query is genuinely in the path of the
write, or the write is refused outright. A function has no equivalent mechanism, and rel has
no way to inspect a function's body at introspection time to distinguish "this is a safe
passthrough" from "this embeds real access control" — so it can't safely allow writes for
some functions and not others either. `query/shape.go`'s `identityIsWritable` enforces this
unconditionally, checked before its `PrimaryKey`/`OnConflict` logic, so a function returning a
real composite/relation type (with a real, otherwise-writable primary key) doesn't
accidentally look writable purely because its underlying table happens to have one.

### Scalar-selected nodes

A node's own `select` isn't required to be shape-producing (`own`/`full`/their variants, an object literal, or a bare `get`/`get-set`) — any other expression (a bare column, an arithmetic expression, a `call`, a bare `agg`, ...) is a valid top-level `select` too, and produces the "distinct shape" `## Response Shape` already gives a scalar (non-`SETOF`) *function* root : one bare JSON value per row, not a one-key object. This is the table-rooted (and embedded-node) analog of that same mechanism — a function root was never the only case with an inherently single, unambiguous value per row ; `select: "name"` on an ordinary relation has exactly the same property.

Applies uniformly at every level a node can appear at, not just the root :

- **Root** : `CompileSelect`/`CompileSelectForDataNode` (the write-then-reread path — they share this, and everything below, through `compileNodeCorrelated`) stream a flat JSON array of scalars instead of an array of objects.
- **To-one embed** : the parent's key for that child becomes a bare value (e.g. `"director": "Denis Villeneuve"`) instead of a nested object.
- **To-many embed** : the parent's key becomes a flat array of scalars (e.g. `"movies": ["A", "B"]`) instead of an array of objects — including when that same child is LATERAL-shared with an `agg` consumer (`## Reading Algorithm`'s own note on multiply-consumed incoming children) : `compileLateralJoin`'s own array materialization switches the same way.

Compiled by `compileNodeCorrelated` itself : a single `to_jsonb(<compiled select expression>) as __scalar` column (`__scalar`, matching this codebase's own `__row_id`/`__node_id`/`__parent_id` convention for an internal name never meant to be a real column or exposed field) in place of the ordinary named-field list `select_fields_for` would otherwise emit — everything else (`from`, `where`, `order by`, `limit`, `distinct`/`distinct on`, LATERAL joins for the node's own incoming children) is unchanged, the same machinery either way. `to_jsonb(...)` isn't optional : the manual `"["/","/"]"` response streaming (`## Response Shape`) writes each row's single column straight through as response bytes, so it has to already be valid JSON regardless of the underlying Postgres type — an unquoted `text` value or a raw composite isn't.

Every caller that turns an already-compiled node into a value (`CompileSelect`, `CompileSelectForDataNode`, `compileEmbedField`'s to-one/to-many wrapping, `compileLateralJoin`'s array materialization) goes through one shared helper (`wrapNodeAsValue`) that picks `row_to_json(alias)` or `alias.__scalar` based on the SAME shape-producing check — there is no second, independently-maintained copy of that decision anywhere.

> Not specially rejected for a WRITE node : the ordinary writability rule (`## Writability` : the identity target must be present, writable, exactly once) already makes a scalar top-level select on the request's own root essentially unwritable in practice — a bare `select: "name"` never includes the primary key, so `identityIsWritable` correctly refuses it, the same as any other identity-omitting select. A scalar select on a *readonly* embedded child (or on a writable child whose own identity is independently satisfied) is unaffected — nothing here changes when writability applies, only what a read produces.

### Scalar hop through a to-one relation

A `.` hop's right side can land on a *different* node than the one doing the selecting — `["own-and", {"director_name": [".", "director", "name"]}]` on a `movie` query, `director` being a joined alias, pulls `director.name` straight into `movie`'s own flat select, with no nested `director` object at all. This is a genuine alternative to embedding the whole child via `join`/`select`, not shorthand for it — the result carries just the one field, at the parent's own top level.

**Resolution allows a `.` hop into any child, to-one or to-many alike** (`## Scoping ### Identifier resolution`'s `resolveExternalHop`) — a chain has more than one downstream use (writability-exclusion tracking among them, `## Writability`'s own note on this), and not all of them need "a single row to pick one field from." **Compiling one as a plain scalar select value is the narrower case, and that's where the to-one restriction actually lives** : reaching a to-many relation this way is a hard compile-time error (`query/sql_expr.go`'s `compileScalarHop`) — there's no single row to pick a field from without aggregating, so use `agg` instead, same restriction `agg` itself enforces in the opposite direction (`agg`'s own target "must be an incoming relation").

Compiles as its own self-contained scalar correlated subquery — `(select <alias>.<col> from <relation> <alias> where <on-clause> and <that relation's own where>)` — structurally the same shape a to-one embed gets, just selecting one column instead of `row_to_json(alias)`. A hop through more than one to-one relation (`movie -> director -> studio`) nests one such subquery per level, recursively. `order by`/`limit`/`distinct` on an intermediate relation are never consulted for this — a to-one relation has at most one matching row by construction (`ResolveJoin`'s own uniqueness requirement, `## Scoping ### Join eligibility`), so there's nothing for them to affect. A target with no matching row (a nullable outgoing FK, unset) reads back as JSON `null` — Postgres's own "a scalar subquery over zero rows is `NULL`" rule, no special-casing needed.

> Not deduplicated against another `.` hop into the *same* relation elsewhere in the same select, nor against that relation also being fully embedded alongside it — each reference compiles its own independent subquery/scan. A LATERAL-sharing optimization mirroring the Reading Algorithm's existing one for a multiply-consumed *incoming* child (`## Reading Algorithm`'s own note on this, `analyzeLaterals`) would remove this, but isn't implemented — a deliberate, known limitation of this mechanism's first pass, not an unnoticed inefficiency.

A hop into a to-one child's own *computed* column (one named explicitly in that child's own `select`, `## Scoping ### Identifier resolution`'s `Shape`-half of `resolveExternalHop`) resolves the same way a plain column does when that computed key itself lands on a `ColumnPath` (e.g. it's itself a `.` chain) — anything else the computed key's expression might resolve to (an arbitrary computed value, not a plain column reference) is not yet supported as a hop target ; the existing "not yet supported" fallback in `compileResolvedField` covers it rather than mis-compiling something.

### Implementation

Per node, recursively :

1. Build the node's own select list from its `select` expression (`own`/`full` and defaulting to `full` when unspecified, `own-except`/`full-and`/etc., or explicit column/expression references), resolved against the node's relation.
2. For each `join` entry, recurse to build the child node's own query, then embed it as a plain correlated scalar subquery in the parent's select list — correlated on `on` (`child.<key> = parent.<value>`, AND'd across a composite `on`, same mapping direction regardless of which side ends up being array or object — see below). No `JOIN`, and no `LATERAL`, needed for this : a subquery in the `SELECT` list can already reference the outer row's columns without it, since `LATERAL` is only required syntax for a *`FROM`-clause* subquery that needs to do the same thing (see the exception in step 5).
3. Whether the embed is a single object or an array follows from whether the join can produce more than one row, not from the outgoing/incoming vocabulary itself — that vocabulary is a proxy for it. Per `query.ts`, a join is also allowed against "distant indexed columns where a unique constraint exists on either the local columns or the parent columns", so a unique constraint on the *joined* side is what actually makes it to-one, and this coincides with (but isn't identical to) "outgoing" for a plain FK join :
   - unique on the joined side → `(select row_to_json(t) from other_relation t where ...)`, giving one object, or SQL `NULL` if there's no match. No `LIMIT 1` needed and no hedging about it : uniqueness here comes from an actual database constraint, so Postgres itself guarantees at most one row — if it didn't, a scalar subquery returning more than one row is a runtime error, which is the correct behaviour for a join rel believes is to-one but the schema doesn't actually back up.
   - not unique on the joined side → `(select coalesce(json_agg(row_to_json(t)), '[]'::json) from (child's own where/order_by/limit/offset) t)`, so "no matches" is an empty array, not `null`.
4. `where`, `order_by`, `distinct`/`distinct_on`, `limit`, `offset` on a node apply directly as ordinary clauses on that node's own subquery. Because it's correlated to its parent row regardless of whether it's phrased as a `SELECT`-list subquery or a `LATERAL` join, a `limit`/`offset` on an embedded (to-many) relation is naturally applied *per parent row* either way — this is the mechanism behind the note in `query.ts` ("When used in a subquery, applies them for each parent-row").
5. **Exception : `LATERAL` is needed when a child relation's rows feed more than one output expression at the parent level.** This happens when an `agg`/`aggregate` expression (`query.ts` : "the expression to aggregate... must be an incoming relation") targets the same relation that's also embedded as an array, or when a node's `select` uses more than one `agg` over the same incoming relation. A `SELECT`-list subquery can only yield a single column, so it can't be reused for both the embedded array and a separate aggregate — and independently re-running the child's subquery for each one isn't just wasteful, it can genuinely disagree with itself : with a `limit`/`offset` and a non-total `order_by`, two separate evaluations of "the same" subquery aren't guaranteed to pick the same rows. In that case, materialize the child's row set once as `LEFT JOIN LATERAL (child subquery, with its own where/order_by/limit/offset applied) t ON TRUE`, and derive every parent-level expression that needs it (the embedded array, each `agg`) from that single `t`, so they're all looking at the same filtered/limited/ordered row set.
6. A `function`-based node (a call rather than a `relation`-named table/view) is handled the same way once its result set is known (see `### Function-rooted nodes` above for how its `Relation` resolves) : table-valued and multi-row behaves like any other joined relation (object vs. array per step 3) ; a scalar, non-set-returning function contributes its result directly, with no `row_to_json`/`json_agg` wrapping — this is also the case referenced in `## Response Shape` ("the scalar of the result of a scalar function").
7. The root node's rows are what get streamed out per `## Response Shape` (`row_to_json` per row, manually delimited) — the root itself never gets its own `json_agg` wrapper, unlike every embedded to-many relation below it.

```sql
-- default case : no aggregate also needs this child's rows, so plain correlated subqueries suffice
select
  -- own/full columns of this node, resolved from introspection
  m.col1, m.col2, /* ... */,
  -- to-one embed : unique on the joined side
  (select row_to_json(t) from other_relation t where t.child_col = m.parent_col /* + t's own where */) as alias1,
  -- to-many embed : not unique on the joined side
  (
    select coalesce(json_agg(row_to_json(t)), '[]'::json)
    from (
      select /* t's own select expression */
      from other_relation t
      where t.child_col = m.parent_col -- from `on`, same mapping direction as above
      order by /* t's own order_by */
      limit /* t's own limit */ offset /* t's own offset */
    ) t
  ) as alias2
from target_relation m
where /* m's own where */
order by /* m's own order_by */
limit /* m's own limit, root only */ offset /* m's own offset, root only */
```

```sql
-- exception : an `agg` at the parent level also needs `orders`' rows, so they're materialized once via LATERAL
select
  m.col1, m.col2, /* ... */,
  coalesce(o.arr, '[]'::json) as orders,   -- the embedded array
  o.total                                  -- e.g. ["agg", "sum", ["orders", "amount"]]
from target_relation m
left join lateral (
  select json_agg(row_to_json(t)) as arr, sum(t.amount) as total
  from (
    select /* orders' own select expression */
    from orders t
    where t.parent_col = m.pk -- from `on`
    order by /* orders' own order_by */
    limit /* orders' own limit */ offset /* orders' own offset */
  ) t
) o on true
```

## Writing Algorithm

### Warnings

* The user may not have permission to write all columns. Queries should not try to write all the object, but always limit themselves to columns they know they can write to
* Default values should be filled whenever not supplied ; we should differenciate the absence of the key from NULL in provided JSON in case when statements

### Definitions

* node : a relation in the query tree, assigned by position in the tree
* current relation : the relation being examined by the algorithm
* _outgoing_ relationship : the current relation's own `on` columns point at a unique set of columns on the other relation. A foreign key is the common way this happens, but not the only one — see `## Scoping ### Join eligibility` : rel's actual eligibility rule is uniqueness + indexing, not "is there a declared FK". The current relation's own columns need no index of their own for this to be valid ; the mandatory index requirement below always falls on the *other* side.
* _incoming_ relationship : the current relation's own `on` columns are the unique side, and the other relation's `on` columns — which point at them — are covered by an index. Again, commonly but not necessarily a declared FK.

These two are exactly `ResolveJoin`'s existing `isToOne` result, viewed from the current node's side of a given edge : outgoing when the *other* side's columns are unique, incoming when *this* side's own columns are unique. They are not a redundant restatement of "is there a foreign key here" — a join can be eligible (and thus be one or the other) without any real FK backing it at all, as long as the uniqueness/indexing shape holds.

Nodes included through `join` in the query are _either_ incoming OR outgoing.


### Implementation

1. rel assigns every node an index value that will be used in the write query. Nodes are identified by tree position, not by table : a self-join produces several distinct nodes for the same table (see note below).
  - For each node, rel also introspects the select expression to determine where to find the columns of the relation ; it creates an "extractor" that will be able to reconstitute a row from the given JSON (and appends it to the big array mentioned afterwards) — see `## Writability` for the exact rules an extractor is built from. This is also where it verifies whether it has enough to perform writes on the table and controls whether it is intended to be readonly or not.
  - Nodes found to be readonly are not processed further (for writing) and are ignored from here on out.
  - For each node and once the columns are known, it will create the corresponding `INSERT` / `UPDATE` / `DELETE` statement that will have to be executed.
  - Delete-bearing write modes (`merge`, `merge-new`, `merge-update`, `deleteonly`) are only valid on an incoming relation. Encountering one of these modes on an outgoing relation is a validation error at this stage. See `query.ts` for the write_mode defaults (root: `insert`, incoming: `merge`, outgoing: `upsert`).

    > Why: only an incoming relation's rows are exclusively scoped to the parent by that indexed reference (FK-backed or not) — an outgoing relation's referenced row may be pointed to by any number of other rows, so there's no coherent set of "rows not in the payload" to delete.

2. denormalize the input ; walk the extractors alongside given data and create one big flat JSON array that will contain all the data to be inserted to the server, using the extractors previously created.

  The request payload's root is always an array of row objects, one per root-level row being written — unlike a nested embed, whose cardinality (a single object vs. an array) comes from its outgoing/incoming classification against its parent, the root has no parent to derive that from : a write request is inherently "here are N rows to write", plural, even when N is 1.

  The temporary table that will house them will be like create temp table _data ( `__node_id` int, `__row_id` int, `__parent_id` int, `data` jsonb, `keys` jsonb ), where `__node_id` is the node index in the query, `__row_id` is an absolute row counter and `__parent_id` is the row_id of the parent node containing the current object. This temporary table should exist for all connections of a pool and be properly truncated whenever a query ends.

  > Question: whether `_data` should be indexed on `__node_id`/`__row_id` is still open. A large payload might benefit from creating those indices after `COPY` loads it, rather than maintaining them incrementally — but for a small payload, a sequential scan can be faster than paying for the index at all. A config option (something like `pg.query.temp_index_threshold`) is one way to make this a size-based decision rather than an always-on or never-on one, but this hasn't been settled ; `_data` is currently created with no indices beyond its `__row_id` primary key (see `query.DataTableDDL`).

  keys will have null initially, but will be populated once the DML statement runs for a given node_id - and will be so _only_ with the needed columns and no more. Depending on the statement (see `### Insertion / Updates` below), this is either the `RETURNING` clause of the DML itself, or a separate `UPDATE ... FROM` against `_data`. `data` itself is never touched.

  > Why: merging into `data` is probably more expensive than just creating the new `keys` object.

  We want __node_id to know which objects are to be used by the DML statement of a given node, and we need row_id <-> parent_id to join the temp table on itself in the DML of dependent tables.

3. **Phase 1 — inserts, updates, upserts.** As data may be inserted/merged that depends on rows not existing yet, these have to be inserted first. The following occurs recursively, per node :

  1. walk the outgoing relationships
  2. perform data modification for the node (insert, update), joining the temp table as necessary to fetch the foreign values that may have been updated thanks to __parent_id, and store the result of the columns that need to be accessed into keys for other nodes (see `### Insertion / Updates` for how keys are actually recovered, which differs between insert, update and upsert).
  3. walk the incoming relationships

  > Why this order: outgoing relationships are the things this node depends on, so writing them first is what makes their generated keys available for step 2. Incoming relationships are the things that depend on this node, so they must come after.

  Note: A relation may self-join : in this case, there are several, distinct nodes. (eg: user that has a manager that is given -> manager is inserted first, then the user. manager is in a subquery node and was resolved as outgoing. recursion stops, because the node with the manager did not do other joins.)

4. **Phase 2 — deletes, deferred until phase 1 has finished for the entire tree.** Every incoming node (or the root) whose write_mode has a delete component (`merge`, `merge-new`, `merge-update`, `deleteonly`) has its "rows not in the payload" delete queued rather than executed inline during phase 3.

  > Why deferred: running every delete only after every insert/update/upsert across the *whole* tree has landed means a row being reassigned from one parent to another (present in the payload under its new parent, absent under its old one) has already been re-pointed by the time its old parent's stale-row delete — and any cascade it triggers — actually runs. Cascade firing on a genuinely-abandoned row at that point is correct and expected, not a bug ; it just never gets the chance to catch a row that was only ever mid-transition.

  Traversal order within phase 2 is the mirror image of phase 1 : a post-order walk of the whole tree, where each node's delete (if it has one) only fires after every node reachable below it — through both incoming *and* outgoing edges — has already had its own delete fire. Concretely: to process a node, first recurse into its incoming children (deepest first), then recurse into its outgoing children, then emit this node's own delete (if it's incoming/root and has a delete-bearing mode). Outgoing nodes never emit a delete themselves, but the traversal still has to walk *through* them, because an outgoing node can itself have incoming children of its own (`user -> manager` (outgoing) `-> manager.direct_reports` (incoming)) that do need their deletes emitted.

  > Why this order: under Postgres's default (non-deferred) foreign key checking, deleting a referenced row before its referencer's stale rows are gone raises an FK violation immediately, so the order is close to forced. It also happens to be the order a delete trigger on a referenced table would naturally expect (dependents already gone before it fires), except under `ON DELETE CASCADE`, which can fire a child's delete trigger from inside the parent's delete and thereby invert this — that's the schema author's choice and not rel's to fight. Pruning the outgoing subtree instead of walking through it would silently skip real deletes on its incoming children.

  The tree defines no order between sibling subtrees that don't reference each other : two delete-bearing nodes on the same physical table reached via unrelated branches have no defined order relative to each other.

  > Why this is safe: phase 1 has already resolved cross-parent reassignment by this point ; the worst case from an unordered pair is an FK violation that errors and rolls back the whole transaction, not silent data loss.

### Insertion / Updates

The default expressions and table column shapes are KNOWN prior to running the algorithm ; the database is introspected at start and on migration reload (or manually by the user.) The pg_ tables must _not_ be used in those queries at request time.

Introspection must cover two distinct sources of "default" per column, not just one :

* plain defaults, from `pg_attrdef` (`nextval('some_seq')`, a literal, `now()`, ...)
* identity columns (`GENERATED ALWAYS | BY DEFAULT AS IDENTITY`), which have no `pg_attrdef` row at all — their backing sequence is found via `pg_get_serial_sequence` (or `pg_depend`), and `pg_attribute.attidentity` tells us `'a'` (ALWAYS) from `'d'` (BY DEFAULT). This distinction matters for insertion, below.

Insert and update statements should only include the columns they intend to modify - those that were found in the exploratory phase.

They will use a rehydrated row from `jsonb_populate_record` that they will re-explode column by column.

Most of the time, they will just use the column as is, but when using a column that's part of an outgoing join's `on` mapping (to a parent, or to an outgoing relation — whether or not it's backed by a declared foreign key), OR when the JSON does not specify a column that has a default value OR when the JSON has a `null` value for a `NOT NULL` column that has a default value, then we use this value instead.

> **Two distinct join directions, not one.** "A column that's part of an outgoing join's `on` mapping" covers two different physical column placements, and the `_data` self-join each needs is a mirror image of the other :
>
> - **This node is itself an incoming child of its own parent** (the common case, e.g. `movie.director_id` referencing `director.id`) : the FK column lives on THIS node, and the value comes from the parent's already-written `keys` — the parent's own `_data` row is found via `par.__row_id = tmp.__parent_id` (the shape shown in the `resolved` CTE example below), an inner join, since a child's row always has a parent row by construction.
> - **This node has an OUTGOING child of its own** (e.g. `user.manager_id` referencing `manager.id`, where `manager` is a value nested inside `user`'s own payload) : the FK column lives on THIS node (the parent, `user`), but the value comes from the CHILD's (`manager`'s) already-written `keys` — found via a left join on the CHILD's own `_data` rows, `oc.__node_id = <child's node id> and oc.__parent_id = tmp.__row_id` (left, not inner : the outgoing relation may be nullable, so the child object may be legitimately absent from the payload).
>
> Both directions read from `keys`, but the join target is different (the literal JSON-tree parent vs. a specific named child), and a node can need either, both, or several of the second kind at once (one per outgoing child actually referenced by a column).

#### Recovering keys : insert vs. update vs. upsert

A node's DML statement must, once it runs, make the identity columns of the rows it touched available as `keys` for its dependents, by writing them into `_data`. The mechanism for doing so is _not_ uniform across the three cases, and picking the wrong one either doesn't compile or silently correlates the wrong row to the wrong key.

> Why it isn't uniform : `INSERT ... SELECT ... FROM _data tmp ... RETURNING`, on its own, cannot reference `tmp`'s columns (only the target table's) — verified directly against Postgres 16 (`ERROR: missing FROM-clause entry for table "tmp"`), so `RETURNING tmp.__row_id, ...` as sketched in an earlier draft of this section does not run. `UPDATE ... FROM ... RETURNING`, by contrast, can freely reference the joined table's columns — this is standard, verified Postgres behaviour, not a special case.

**Plain `insert`** (no `on_conflict` target in play) : resolve the whole row — including any identity/sequence-backed default — in a CTE first, so the value is known _before_ the physical insert, then insert from that CTE (no `RETURNING` needed) and separately `UPDATE _data ... FROM` the same CTE to fill in `keys`. This is legacy's `json_flat.pat` technique, and matches it deliberately.

```sql
with resolved as (
  select
    tmp.__row_id,
    -- for each column, resolve to the value that will actually be inserted
    case
      when /* column is a FK to a parent or outgoing node written earlier this transaction */
        then (par.keys->>'colname')::coltype
      when /* 'colname' absent from tmp.data, or explicit null on a NOT NULL column, and colname has a default */
        then <default expression, e.g. nextval('target_relation_id_seq')>
      else obj.colname
    end as colname
    -- ... one such expression per impacted column
  from _data tmp
  join lateral jsonb_populate_record(null::target_relation, tmp.data) as obj on true
  -- present only when this node has an outgoing/parent dependency written earlier in phase 1 ;
  -- par is guaranteed to exist by the phase 1 traversal order, so this is an inner join
  inner join _data par on par.__row_id = tmp.__parent_id
  where tmp.__node_id = /* the node we're doing now */
),
ins as (
  insert into target_relation (col1, col2, ...)
  -- OVERRIDING SYSTEM VALUE is required whenever an identity ('a', ALWAYS) column is being
  -- explicitly populated ; harmless to include when there are none.
  overriding system value
  select col1, col2, ... from resolved
)
update _data
set keys = r.keys
from (select __row_id, jsonb_build_object(/* pk/unique columns */) as keys from resolved) r
where _data.__row_id = r.__row_id;
```

> Why pre-compute instead of letting Postgres generate the default at insert time : it's the only way to have the generated key available for `keys` without relying on cross-table `RETURNING`, which doesn't exist for plain `INSERT`. Confirmed the `resolved` CTE isn't re-evaluated (and `nextval()` not called twice) between the `insert` and the `keys` `update` — both read the same materialized CTE, and a CTE referenced more than once is never inlined regardless.

**`update`** (row identified by an already-known key from the payload) : a single `UPDATE ... FROM ... RETURNING` suffices, since the identity is known before the statement runs and RETURNING-off-an-UPDATE can reference the FROM-joined columns directly :

```sql
update target_relation t
set col1 = resolved.col1, col2 = resolved.col2, ...
from (
  select tmp.__row_id, obj.colname /* ... same per-column resolution as above ... */
  from _data tmp
  join lateral jsonb_populate_record(null::target_relation, tmp.data) as obj on true
  where tmp.__node_id = /* the node we're doing now */
) resolved
where t.pk_col = resolved.pk_col
returning resolved.__row_id, jsonb_build_object(/* pk/unique columns */) as keys
-- the caller writes this RETURNING output into _data.keys directly, no separate statement needed
```

**`upsert`** (`on_conflict (...) do update`) : the pre-computation trick from the `insert` case does not apply, because a row that *does* conflict keeps its existing identity, not the pre-computed one. Instead, use `RETURNING` off the `INSERT ... ON CONFLICT DO UPDATE` itself — this is legal, since it only returns target-table columns — and correlate back to `_data` using the `on_conflict` columns, which are known pre-insert because they came straight from the payload (unlike a generated PK) :

```sql
with dml as (
  insert into target_relation (col1, col2, ...)
  select col1, col2, ... from resolved  -- resolved as in the plain-insert case, minus the identity pre-computation
  on conflict (conflict_col) do update set col2 = excluded.col2, ...
  returning pk_col, conflict_col
)
update _data
set keys = jsonb_build_object(/* pk/unique columns */, 'pk_col', dml.pk_col)
from resolved
join dml on dml.conflict_col = resolved.conflict_col
where _data.__row_id = resolved.__row_id;
```

There is no `insert ... on conflict do update` against `_data` itself anywhere in this section — `_data`'s rows already exist from the `COPY` in step 2 of `### Implementation`, so writing `keys` back into it is always a plain `update`, never an upsert.

#### `write_mode` → DML mechanism

The three mechanisms above are named for their SQL shape (`insert`/`update`/`upsert`), not 1:1 for the seven `write_mode` values `query.ts` defines — several modes share a mechanism, and one (`merge-new`) needs a fourth shape none of the three above cover :

| `write_mode` | phase 1 mechanism | phase 2 delete |
|---|---|---|
| `readonly` | none — node and its subtree are skipped entirely for writing | no |
| `insert` | plain insert | no |
| `update` | plain update | no |
| `upsert` | upsert (`on conflict ... do update`) | no |
| `merge` | upsert | yes |
| `merge-update` | plain update | yes |
| `merge-new` | insert `on conflict (...) do nothing` (see below) | yes |
| `deleteonly` | none | yes |

**`merge-new`'s insert-do-nothing shape.** "Insert new ones, don't update existing ones" can't reuse the plain-insert mechanism as-is : an existing conflicting row would otherwise raise a unique-violation error rather than being left alone. It needs `on conflict (identity columns) do nothing`, plus a wrinkle the other three mechanisms don't have — a conflicting row contributes no `RETURNING` output at all, so its `resolved.colname` values (including any freshly precomputed sequence default) are never actually applied and must not be trusted as `keys`. Two things follow :

1. `insert ... on conflict (...) do nothing returning <identity + whatever a child needs>` still lets a genuinely-new row's keys be recovered correctly (a row that WAS inserted does return them), joined back to `_data` the same way the plain-insert case joins its `resolved` CTE.
2. A conflicting row's keys must be recovered separately, by matching `_data`'s own payload (the on_conflict columns, always present verbatim — see the writability note below) against the real, already-existing row in the target table directly — not through `resolved` at all, since `resolved`'s precomputed values for that row are exactly the phantom (never-applied) ones from point 1.

Both scans are needed because neither one alone sees every row : the `RETURNING`-based one only sees rows that were actually inserted, the table-matching one only reliably identifies rows via columns known before the insert ran (i.e. the payload's own on_conflict columns) — a freshly-inserted row's *other* identity columns (e.g. a generated primary key when `on_conflict` targets a different unique column) aren't yet knowable that way until the row exists, which is exactly what the `RETURNING` scan already gives you for free.

**Recovered `keys` isn't always just the identity/on_conflict columns.** If a node has an outgoing child of its own (see the two-join-directions note above), that child needs THIS node's keys to include whichever column ITS `on` mapping targets — which is not necessarily this node's `on_conflict` set (e.g. `on_conflict: [user_email]` while a child instead correlates via the primary key `id`). `keys` must be the union of the identity/on_conflict set and every column any child's own `on` mapping targets on this node, or that child's own recovery reads `NULL` for a value that does exist. The symmetric case also holds : when this node is itself an outgoing child, its OWN `keys` must include whichever of its own columns its parent's `on` mapping names, even when that isn't this node's own identity/on_conflict column either.

### Note about merges

Rel does not use the native Postgres `MERGE` statement ; it stays on plain `INSERT` / `UPDATE` / `DELETE`.

> Why: `MERGE` is comparatively recent and fairly slow. Plain INSERT/UPDATE/DELETE keeps speed, compatibility, and simplicity.

## Errors

HTTP status >= 400. The numeric status lives only in the HTTP response line itself, never repeated in the body. `400` is used for a problem with the query/data itself (unknown relation, malformed query string, a compile-time rejection, ...) ; `500` for everything else. `401` is used when anonymous access is disabled outright and the request carries no usable credentials. An `RSxxx` status (`rpc.md`'s convention) is the one other status this envelope carries, raised by `http.functions.check_session`.

The response body's exact shape (`RelErrorResponse`), the `code` taxonomy, and what's included under `dev` mode (`pg_error`, `stacktrace`) are specified in full in `error-handling.md` — kept there rather than duplicated here now that it covers both `/rel` and `/rpc` uniformly, not just this document's own concern.

## Query Shape

See `./query.ts` for the JSON query shape.

## Response Shape

On success, rel returns the result of `row_to_json` for each row returned, or the scalar of the result of a scalar function (ie, not set-returning) as JSON.

For performance reasons, there is no need to have postgres build a BIG json array as a result ; when the result will be a big array, rel outputs the `[` `]` and `,` manually, iterating over the rows and writing each one to the client directly, instead of having Postgres aggregate them (e.g. via `json_agg`) into one large value first.

> Why this is sensible : `json_agg`-ing server-side means Postgres has to hold the entire result in memory as one growing value before sending anything, and the app then has to hold it again before it can write the first byte — manual streaming keeps both sides at roughly constant memory and lets the client start receiving data before the query has finished.

For a write query, the entire write algorithm — phase 1 and phase 2 from `## Writing Algorithm` — runs first, for every query in the request. The response's own read-back statements then run for every item (write items reading back what they just wrote, read items running their own query), still inside that SAME transaction — see `## Transactions` for why this now covers reads too, not just writes. `COMMIT` is the very last thing the whole request does, only once every item has fully streamed successfully ; nothing about response-building runs in a separate, later transaction or statement anymore.

This still has a consequence for `_data` (see `## Writing Algorithm` step 2) : it must be created `ON COMMIT PRESERVE ROWS`, not `ON COMMIT DROP` — not because the read-back needs to survive a commit that no longer happens before it runs, but because the deferred final cleanup (`truncate _data`, once per request, after the response has been fully sent) runs as its own statement AFTER the single commit above ; `ON COMMIT DROP` would drop the table out from under that cleanup step, which expects it to still exist (`create temp table if not exists` at the start of the next request would still recover from that, but the truncate call itself would fail first). And when a request bundles several queries sharing one transaction, none of their responses start streaming until every write item across the whole `Sequence` has finished phase 1/2 — reads and read-backs run afterward, in item order, still before the single commit.
