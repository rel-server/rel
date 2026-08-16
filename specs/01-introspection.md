# Introspection

After running migrations/mutations, and prior to serving HTTP requests, rel introspects the Postgres server it will serve requests from, building an in-memory picture of every relation, constraint, index, function, and type it will need to compile queries against.

Whenever mutations are re-run (via the `SIGUSR1` signal), the schema is reintrospected afterwards.

> Question : the original wording here said the HTTP server is shut down and restarted on reload. That's not specified anywhere else, and it doesn't say what happens to requests that arrive during that window (queued ? rejected ? served against the stale schema until the swap completes ?). This needs its own decision, tied to how the connection pool and request lifecycle actually work — neither of which has a spec yet (see `TODO.md`).

## What gets introspected

Implemented in the `pg` package (`github.com/ceymard/rel/pg`), tested against a real Postgres instance (`pg/info_test.go`, `pg/testdata/schema.sql`) rather than specified in the abstract — this section documents the contract that package actually provides, so the query compiler (and anything else built against it) can rely on it without re-reading the Go source.

`pg.NewInfos(uri string) (*DbInfos, error)` opens a connection and fills a `*DbInfos` in one pass, in this order : functions, relations, constraints, indexes, types. Constraints and indexes are correlated to relations by Postgres OID (`PgRelId`), so they run after relations ; types run last because they read back into both functions and relations to resolve `PgTypeOid`/`PgReturnTypeOid` references.

Introspection is deliberately unfiltered by schema — `pg_catalog` and `information_schema` are introspected exactly like any other schema, not excluded. This is intentionally separate from `querying.md ### Scoping`'s relation blacklist, which *does* refuse `pg_catalog`/`information_schema` as query targets — that's a compile-time check against a fully-known schema, not a reason to make introspection itself blind to those schemas.

> Why : introspection needs the complete picture of the database, not just the part a query is allowed to touch. A function can return a `pg_catalog` composite type, or `SETOF` a system view — resolving that return type (`Type.IsComposite()`/`Type.Relation`, needed for TypeScript generation among other things) requires the backing relation to have actually been introspected, regardless of whether a client query could ever select from it directly. An earlier version of this file excluded both schemas at introspection time on the reasoning that they're never valid query targets anyway ; that conflated two different concerns; and it silently broke exactly this case, since a relation that was never introspected can't be resolved by `Type.Relation` no matter how that lookup is implemented.

### Relations, columns

`Relation` carries its columns (`Columns []*Column`, plus `ColumnsMap` for name lookup), its resolved composite `Type`, and everything constraint/index-related described below.

`Column` carries `Name`, `PgTypeOid`/`Type`, `DefaultExpression` (already resolved to a spliceable SQL expression — see below), `IsIdentity`, `IsNullable`, `IsUpdatable`.

Relations, columns, functions, and types (`### Types`, below) each carry their own `Comment string`, sourced from `COMMENT ON` (`obj_description`/`col_description`), empty when unset. These exist for the TypeScript export to surface as doc comments on the generated types — not consumed by anything else yet. Constraints and indexes do not carry comments — nothing surfaces them in TypeScript output, so there was no reason to introspect them.

> Why `col_description` is indexed by `information_schema.columns.ordinal_position` directly, with no separate `pg_attribute` join for the real `attnum` : verified directly against Postgres 16, including with a dropped column ahead of the target one, that `ordinal_position` already **is** the real `attnum` (gaps from drops included, not renumbered) — so the two are interchangeable for this lookup, and the extra join isn't needed.

`Column.IsPrimaryKey`, `IsGenerated`, `IsParOfUnique`, and `IsNotNull` are declared fields that nothing currently populates — they read as their zero value always. `IsReallyNotNull()` is consequently incomplete : it only reflects `Type.PgDomainNotNull` (a domain's own not-null constraint), never the column's own `NOT NULL`. The introspection query also computes a `DomainIdentifier` object with no corresponding `Column` field to land in, silently dropped by `json.Unmarshal`.

> Question : should these four fields be wired up (`IsNotNull` from `information_schema.columns.is_nullable`, `IsPrimaryKey`/`IsParOfUnique` from the relation's own constraints, `IsGenerated` from `is_generated`/`generation_expression`), or are they dead and should be removed ? Nothing built so far depends on them either way.

`DefaultExpression` is resolved from two distinct sources, not one : `pg_attrdef` for plain defaults (`nextval(...)`, a literal, `now()`, ...), and identity columns (`GENERATED ALWAYS | BY DEFAULT AS IDENTITY`) via `pg_get_serial_sequence`, since identity columns have no `pg_attrdef` row at all. See `querying.md ### Insertion / Updates` for how this feeds the write algorithm's default-value splicing, including the `OVERRIDING SYSTEM VALUE` requirement for `GENERATED ALWAYS` columns.

### Constraints

`Constraint` represents a `PRIMARY KEY`/`UNIQUE`, or one side of a foreign key. `ConstraintType` is one of `ConstraintTypePrimaryKey`, `ConstraintTypeUnique`, `ConstraintTypeOutgoingForeignKey`, `ConstraintTypeIncomingForeignKey` — the outgoing/incoming vocabulary matches `querying.md ### Definitions` exactly. For a foreign key, `Constraint.Target` is the reciprocal `Constraint` living on the other relation ; either side reaches the other via `.Target`, and `.Target.Relation` (or the `OtherRelation()` helper) is always "the other relation" regardless of direction.

`Constraint.Columns` is in true declared order (from `pg_constraint.conkey`/`confkey`), and `Columns[i]` corresponds to `Target.Columns[i]` for a foreign key. This pairing must never be reconstructed from anywhere else — in particular, never from two independently-sorted column-name lists, which is not guaranteed to reproduce the true correspondence for a composite key.

> Why `pg_constraint` and not `information_schema` : `information_schema.constraint_column_usage` sorts its target columns alphabetically by name, independent of the source side's declared order — verified directly against Postgres 16 with a composite FK whose true pairing doesn't happen to match alphabetical order on both sides. `pg_constraint.conkey`/`confkey` are position-correlated arrays and don't have this problem.

Relations do not expose their constraint maps directly. The only public surface is the lookup API below — canonicalization (sorting a column set into a comparable key) happens once, inside the package, never in calling code.

### Indexes

`Index` is a distinct capability from constraints, not folded into them — a table's index inventory and its constraint inventory are related but separate facts, and the join-eligibility rule (`querying.md ### Join eligibility`) needs both. `Index.Columns` is the true leading-key-column order, already filtered to exclude what doesn't count for an equality lookup :

- Only the first `indnkeyatts` columns of `pg_index.indkey` — an `INCLUDE`d column (covering index) sits past that boundary and cannot serve the lookup itself.
- Partial indexes (`indpred IS NOT NULL`) are excluded entirely — they don't provably cover a query's actual row set.
- Expression indexes (`indexprs IS NOT NULL`, `indkey` carrying a `0` at the expression's position) are excluded — `on` only ever joins on plain columns.

> Why : all three exclusions were verified directly against Postgres 16 rather than assumed — an `INCLUDE`d column, a partial index, and an expression index were each created and inspected through `pg_index` to confirm `indnkeyatts`, `indpred`, and `indkey`'s `0`-marker behave as described.

### Types

`Type` resolves the full `pg_type` graph — `ElementType`/`ArrayType` (subscripting/array-of relationships), `BaseType` (domains), and `Relation` for composite types, via `IsArray()`/`IsDomain()`/`IsComposite()`. This resolution has to be complete and correct regardless of schema, not just for `Column`/`FunctionArgument` types living in ordinary schemas : a function can return a `pg_catalog` composite type or `SETOF` a system view, and generating a correct TypeScript type for that return value needs `Type.Relation` to resolve into a real, introspected `Relation` the same way it would for an ordinary table — knowing only columns, tables, and views is not enough on its own. TypeScript/schema export itself isn't specified yet (see `TODO.md`), but the type graph it will need to walk is built here.

> Why this needed a fix, not just unfiltered introspection : `Type.Relation` was a declared field that nothing ever set, and the loop meant to resolve `ElementType`/`ArrayType`/`BaseType` was ranging over `infos.Types` by value in a fresh loop, mutating a throwaway copy each time rather than the object `infos.TypeMapByOid` actually pointed to — the same class of bug as the `FunctionArgument`/`Type` fix above, just with no test yet to catch it (every type silently reported `IsArray()`/`IsDomain()`/`IsComposite()` as false, always). Fixed alongside the introspection-scope change, since making `pg_catalog` visible would have been pointless while `Type.Relation` still could never resolve. Regression-tested : `pg/info_test.go`, `TestType_CompositeResolvesBackToRelation`, `TestType_ArrayResolution`.

### Functions

`Function` carries `Identifier`, `Arguments []FunctionArgument`, `ReturnType`/`ReturnsSet`, and the usual volatility/strictness flags. Argument resolution falls back from `pg_proc.proallargtypes`/`proargmodes` to `proargtypes` (renormalized from its 0-indexed `oidvector` form) when the former are `NULL`.

> Why the fallback : `proallargtypes`/`proargmodes` are only populated by Postgres when a function has at least one `OUT`/`INOUT`/`VARIADIC`/`TABLE` argument — for a plain function with only `IN` arguments (the common case, verified directly against Postgres 16), both are `NULL`, and without the fallback `Arguments` comes back empty. Regression-tested : `pg/info_test.go`, `TestFunctionArguments_PlainInArgs`.

## The lookup API

The only way calling code (the query compiler) is meant to consult a relation's constraints and indexes — no exported raw maps, so there's nowhere to accidentally reimplement canonicalization incorrectly.

```go
func (r *Relation) FindConstraintByName(name string) *Constraint
func (r *Relation) FindUniqueConstraint(columns []string) *Constraint
func (r *Relation) RelationshipsTo(other *Relation) []*Constraint
func (r *Relation) IsIndexed(columns []string) bool

func (r *Relation) ResolveJoin(parent *Relation, on map[string]string) (constraint *Constraint, isToOne bool, err error)
```

`ResolveJoin` is the one the query compiler actually calls per join in a query tree, given `r` (the relation being described, i.e. the child, per `query.ts`'s `on` field) and `parent` (the enclosing relation). It implements `querying.md ### Join eligibility` in full :

- Cardinality (`isToOne`) is decided purely by whether `r`'s own `on` columns are unique on `r` — independent of whether the relationship is foreign-key-backed, and independent of which side satisfies eligibility below (`querying.md ## Reading Algorithm`, "unique on the joined side -> object").
- Eligibility requires either a foreign key whose exact column pairing matches `on` (not just matching column sets on each side independently — a same-sets-different-pairing mapping is rejected), or a unique constraint on `r`'s columns or on `parent`'s columns.
- Indexing on `r`'s own `on` columns is checked unconditionally and is a hard error if missing, no config escape hatch — the generated query always scans `r` filtered by them, once per parent row, regardless of cardinality.

> Question : the non-FK eligibility branch checks column sets independently on each side, with no requirement that they correspond to each other. Since a foreign key's target is required to be backed by a unique constraint on exactly its column set, any permutation of a pairing over that same set is accepted as eligible via the non-FK path — even when a real FK exists between the same two relations and the permutation contradicts its actual declared correspondence. Same open question as `querying.md ### Join eligibility` ; not duplicated in full here, see there for the complete writeup and the regression test that found it (`pg/info_test.go`, `TestForeignKey_CompositePairing`).

## Testing

`pg/testdata/schema.sql` plus `pg/info_test.go` is the first instance of the testcontainers pattern `AGENTS.md` requires — one shared Postgres container per test run (`TestMain`), a fixture schema built to exercise specific known-tricky cases (composite FK pairing, `INCLUDE`/partial/expression index exclusion, multiple distinct FKs between the same two relations, the non-FK eligibility path, `pg_catalog` inclusion, composite/array type resolution, comment introspection) rather than a generic sample schema. Not yet written up as a general convention for the rest of the project — see `TODO.md`, "Testing conventions."
