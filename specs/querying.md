# Querying

Rel's most important feature is its querying capabilities.

Similarly to GraphQL and PostgresT, it offers a complex query engine able to span across relations in the database to produce intricated and complex content.

Unlike GraphQL, there are no "mutations" to describe ; unlike PostgresT, the complex form that it generates can be sent back as-is to the server so that it updates accordingly ; it makes inserting or updating rows of linked tables in a single transaction possible, even and especially when related rows depend on a identifying key not yet known (ids) because the parent doesn't already exist.

Selecting is based on a relation. Foreign keys allow embedding of a distant resource into the result : whether from the table to another or in reverse. When embedding a remote relation that has multiple rows to the current one, embeds an array. Otherwise, stays as a simple object.

## Search path

Roles we switch to when requests are made are NOT respected, because that would mean having to query them every time. The only enforced search path will be the one of the base user we connect the database to.

## Configuration

* `query.user` (default : `dmut.user` if provided) the login role rel will connect with. This is the role from which `set role` to all other roles will be executed from. 
* `query.password` (default: `dmut.password` if provided) its password

* `query.maxdepth` (default `6`) : maximum depth a query can specify

Unlike route functions, all queries are sent on `/rel`, and all of them MUST be `POST`.

When querying resources they're not allowed to access in the database, the status will be `401`. A unknown relation will result in `400`, as `/rel` will never be 404 itself.

`/rel` only returns JSON, even when it replies an error.

For a query to be bidirectional, there is a notion of writability of a column ; a column is said to be writable if and only if it appears exactly once in the select expression and is not transformed by anything other than coalescing operators. Columns are tracked and are writable even if they appear in sub-objects.

A relation's rows are writable iff the columns of its identity target (primary key by default, or whatever on_conflict explicitly designates as the  conflict-resolution unique constraint) are present and writable exactly *once* in the select output.

If a child query disables writability for its own table, it disables it for the whole query, unless it was *explicitely* set to readonly. A user attempting a write on such a query receives an error indicating the offending relation. 

`distinct` and `distinct_on` do not need to disable writability on their own ; the general contract two paragraphs up (identity target present and writable exactly once) already covers the only case that would actually be dangerous.

> Why : you're not confused, this is right. Plain `distinct` deduplicates on the *entire* projected row. If the identity target is part of that row, two different underlying source rows can never produce equal tuples in the first place — the identity columns alone already guarantee tuple inequality between them — so `distinct` can never actually merge two source rows together when identity is present ; it's a no-op with respect to row correspondence. It only becomes dangerous when identity is *absent* from the select (e.g. `select distinct city`, where one output row could stand for many different users) — and that case is already excluded by the general rule regardless of `distinct`. `distinct_on` reasons the same way : it doesn't merge rows either, it picks exactly one real row per group (via `order_by`), so the identity columns on that output row still correctly name the one real row it came from.

Group by and window functions are intentionally disabled ; these should be done inside views instead.

> Why: too much abuse potential, and group by anyway disables writing back entirely on the relation. Rel is about selecting data to write it back (mostly,) the rest can be done in views.

To avoid paying for parsing and preparing statements all the time, rel offers a "well-known" queries mechanism that are read on server startup or after reloading a schema. They are named and make use of the `["$param", ...]` expresion which are transformed into prepared statement param. Another advantage of well-known queries is that they're also exported by rel's typescript/javascript export and typed appropriately.

## Implementation details

* Use github.com/bytedance/sonic
* Do not use struct tags ; JSON must be parsed using .Get and other iterative methods for performance
* Send JSON to postgres, (ab)use json(b)_populate_record and json(b)_populate_recordset
* Use COPY to send the JSON to TEMP tables in postgres

## Transactions

A query or several queries (in one HTTP request) run in a single transaction ; any error stops and rollbacks everything.

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

The database role rel connects with to the server in order to perform requests should never be `postgres` or superuser, and should never be a member of `pg_read_server_files`, `pg_write_server_files`, `pg_execute_server_program`, or `pg_signal_backend` - a stark warning must be printed if this is the case. The developer must be incited to create a role of some kind that will receive grants for all subroles that shall exist within the database and give it to `query.user`.

> Why this still matters alongside the blacklist : the blacklist can only stop what it already knows the name of. It's a maintained list, not a closed one — a newly `CREATE EXTENSION`'d function (which defaults to `PUBLIC EXECUTE` the moment it's created, e.g. `dblink`, `postgres_fdw`) isn't covered until someone notices and adds it. The role restrictions above are the backstop for exactly that gap : as long as the role never holds those privileges/memberships, most of what makes a *newly discovered* dangerous function actually dangerous (arbitrary file/network/process access) stays unreachable regardless of whether the blacklist has caught up yet.


## Reading Algorithm

Much simpler than writing : no phases, no `_data` temp table, no dependency ordering — one recursive walk of the query tree that emits a single correlated `SELECT`, per node.

### Implementation

Per node, recursively :

1. Build the node's own select list from its `select` expression (`own`/`full` and defaulting to `full` when unspecified, `own-except`/`full-and`/etc., or explicit column/expression references), resolved against the node's relation.
2. For each `join` entry, recurse to build the child node's own query, then embed it as a plain correlated scalar subquery in the parent's select list — correlated on `on` (`child.<key> = parent.<value>`, AND'd across a composite `on`, same mapping direction regardless of which side ends up being array or object — see below). No `JOIN`, and no `LATERAL`, needed for this : a subquery in the `SELECT` list can already reference the outer row's columns without it, since `LATERAL` is only required syntax for a *`FROM`-clause* subquery that needs to do the same thing (see the exception in step 5).
3. Whether the embed is a single object or an array follows from whether the join can produce more than one row, not from the outgoing/incoming vocabulary itself — that vocabulary is a proxy for it. Per `query.ts`, a join is also allowed against "distant indexed columns where a unique constraint exists on either the local columns or the parent columns", so a unique constraint on the *joined* side is what actually makes it to-one, and this coincides with (but isn't identical to) "outgoing" for a plain FK join :
   - unique on the joined side → `(select row_to_json(t) from other_relation t where ...)`, giving one object, or SQL `NULL` if there's no match. No `LIMIT 1` needed and no hedging about it : uniqueness here comes from an actual database constraint, so Postgres itself guarantees at most one row — if it didn't, a scalar subquery returning more than one row is a runtime error, which is the correct behaviour for a join rel believes is to-one but the schema doesn't actually back up.
   - not unique on the joined side → `(select coalesce(json_agg(row_to_json(t)), '[]'::json) from (child's own where/order_by/limit/offset) t)`, so "no matches" is an empty array, not `null`.
4. `where`, `order_by`, `distinct`/`distinct_on`, `limit`, `offset` on a node apply directly as ordinary clauses on that node's own subquery. Because it's correlated to its parent row regardless of whether it's phrased as a `SELECT`-list subquery or a `LATERAL` join, a `limit`/`offset` on an embedded (to-many) relation is naturally applied *per parent row* either way — this is the mechanism behind the note in `query.ts` ("When used in a subquery, applies them for each parent-row").
5. **Exception : `LATERAL` is needed when a child relation's rows feed more than one output expression at the parent level.** This happens when an `agg`/`aggregate` expression (`query.ts` : "the expression to aggregate... must be an incoming relation") targets the same relation that's also embedded as an array, or when a node's `select` uses more than one `agg` over the same incoming relation. A `SELECT`-list subquery can only yield a single column, so it can't be reused for both the embedded array and a separate aggregate — and independently re-running the child's subquery for each one isn't just wasteful, it can genuinely disagree with itself : with a `limit`/`offset` and a non-total `order_by`, two separate evaluations of "the same" subquery aren't guaranteed to pick the same rows. In that case, materialize the child's row set once as `LEFT JOIN LATERAL (child subquery, with its own where/order_by/limit/offset applied) t ON TRUE`, and derive every parent-level expression that needs it (the embedded array, each `agg`) from that single `t`, so they're all looking at the same filtered/limited/ordered row set.
6. A relation with `arguments` (a function call rather than a table) is handled the same way once its result set is known : table-valued and multi-row behaves like any other joined relation (object vs. array per step 3) ; a scalar, non-set-returning function contributes its result directly, with no `row_to_json`/`json_agg` wrapping — this is also the case referenced in `## Response Shape` ("the scalar of the result of a scalar function").
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

### Computed columns

`own`/`full` (and their `-except`/`-and` variants) enumerate physical columns only, sourced from `pg_attribute` at introspection time — they never implicitly pull in a computed column (a function taking the relation's row type as its argument, callable via `alias.func_name` or `func_name(alias)` in Postgres). A computed column is only included when named explicitly in `select`, at which point it resolves through the same function-identifier path — and is subject to the same `## Scoping` rules — as any other `["call", ...]`. It is never a candidate for writability (`## Configuration`), since it isn't a real column to begin with.

Expression columns are not write candidates for similarly obvious reasons.

## Writing Algorithm

### Warnings

* The user may not have permission to write all columns. Queries should not try to write all the object, but always limit themselves to columns they know they can write to
* Default values should be filled whenever not supplied ; we should differenciate the absence of the key from NULL in provided JSON in case when statements

### Definitions

* node : a relation in the query tree, assigned by position in the tree
* current relation : the relation being examined by the algorithm
* _outgoing_ relationship : the current relation has a foreign key that points to another relation
* _incoming_ relationship : a relation has a foreign key on the primary key or another unique set of columns to the current relation

Nodes included through `join` in the query are _either_ incoming OR outgoing.


### Implementation

1. rel assigns every node an index value that will be used in the write query. Nodes are identified by tree position, not by table : a self-join produces several distinct nodes for the same table (see note below).
  - For each node, rel also introspects the select expression to determine where to find the columns of the relation ; it creates an "extractor" that will be able to reconstitute a row from the given JSON (and appends it to the big array mentioned afterwards). This is also where it verifies whether it has enough to perform writes on the table and controls whether it is intended to be readonly or not.
  - Nodes found to be readonly are not processed further (for writing) and are ignored from here on out.
  - For each node and once the columns are known, it will create the corresponding `INSERT` / `UPDATE` / `DELETE` statement that will have to be executed.
  - Delete-bearing write modes (`merge`, `merge-new`, `merge-update`, `deleteonly`) are only valid on an incoming relation. Encountering one of these modes on an outgoing relation is a validation error at this stage. See `query.ts` for the write_mode defaults (root: `insert`, incoming: `merge`, outgoing: `upsert`).

    > Why: only an incoming relation's rows are exclusively scoped to the parent by the FK — an outgoing relation's referenced row may be pointed to by any number of other rows, so there's no coherent set of "rows not in the payload" to delete.

2. denormalize the input ; walk the extractors alongside given data and create one big flat JSON array that will contain all the data to be inserted to the server, using the extractors previously created.

  The temporary table that will house them will be like create temp table _data ( `__node_id` int, `__row_id` int, `__parent_id` int, `data` jsonb, `keys` jsonb ), where `__node_id` is the node index in the query, `__row_id` is an absolute row counter and `__parent_id` is the row_id of the parent node containing the current object. This temporary table should exist for all connections of a pool and be properly truncated whenever a query ends. (I'm torn on indexing this table ; maybe the indices on node_id and row_id could be created once COPY is done if there are many rows in this table - sometimes seq scanning is faster. maybe a configuration option like `query.tempindexthreshold` ?)

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

Most of the time, they will just use the column as is, but when using a column that is a FK to another relation (to a parent, or to an outgoing relation), OR when the JSON does not specify a column that has a default value OR when the JSON has a `null` value for a `NOT NULL` column that has a default value, then we use this value instead.

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

> Note: there is no `insert ... on conflict do update` against `_data` itself anywhere in this section — `_data`'s rows already exist from the `COPY` in step 2 of `### Implementation`, so writing `keys` back into it is always a plain `update`, never an upsert.

### Note about merges

Rel does not use the native Postgres `MERGE` statement ; it stays on plain `INSERT` / `UPDATE` / `DELETE`.

> Why: `MERGE` is comparatively recent and fairly slow. Plain INSERT/UPDATE/DELETE keeps speed, compatibility, and simplicity.

## Errors

HTTP status >= 400

Returns a JSON object with

```typescript
interface RelErrorResponse {
  status_code: number // http
  error: string // error code, to be documented
  message: string
  pg_error?: /* pg fields */
  stacktrace?: Frame[] // maybe frame is just a string, unclear at this moment
  sql_statement?: string // in debug mode, give the faulty SQL statement
  data?: any // in debug mode, maybe behind a config option, to know what data did cause the crash - the flat node that we were trying to insert, for instance, especially useful when debugging
}
```

## Query Shape

See `./query.ts` for the JSON query shape.

## Response Shape

On success, rel returns the result of `row_to_json` for each row returned, or the scalar of the result of a scalar function (ie, not set-returning) as JSON.

For performance reasons, there is no need to have postgres build a BIG json array as a result ; when the result will be a big array, rel outputs the `[` `]` and `,` manually, iterating over the rows and writing each one to the client directly, instead of having Postgres aggregate them (e.g. via `json_agg`) into one large value first.

> Why this is sensible : `json_agg`-ing server-side means Postgres has to hold the entire result in memory as one growing value before sending anything, and the app then has to hold it again before it can write the first byte — manual streaming keeps both sides at roughly constant memory and lets the client start receiving data before the query has finished.

For a write query, the entire write algorithm — phase 1 and phase 2 from `## Writing Algorithm`, for every query in the request (several queries in one HTTP request share a single transaction, see `## Transactions`) — runs and `COMMIT`s _before_ any selection work for the response begins. The response is always built from a separate, read-only statement issued after that commit, never from inside the write transaction.

This has two consequences for `_data` (see `## Writing Algorithm` step 2) : it must be created `ON COMMIT PRESERVE ROWS`, not dropped or truncated at the write transaction's commit, since the read-back statement that builds the response needs to join against it (in particular against `keys`, populated during phase 1) on the same connection ; truncation happens once, at the very end of the whole request, after the response has been fully sent. And when a request bundles several queries sharing one transaction, none of their responses start streaming until that single shared commit lands, not as each query's own writes finish.
