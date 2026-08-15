# Querying

Rel's most important feature is its querying capabilities.

Similarly to GraphQL and PostgresT, it offers a complex query engine able to span across relations in the database to produce intricated and complex content.

Unlike GraphQL, there are no "mutations" to describe ; unlike PostgresT, the complex form that it generates can be sent back as-is to the server so that it updates accordingly ; it makes inserting or updating rows of linked tables in a single transaction possible, even and especially when related rows depend on a identifying key not yet known (ids) because the parent doesn't already exist.

Selecting is based on a relation. Foreign keys allow embedding of a distant resource into the result : whether from the table to another or in reverse. When embedding a remote relation that has multiple rows to the current one, embeds an array. Otherwise, stays as a simple object.

## Configuration

* `query.maxdepth` (default `6`) : maximum depth a query can specify

Unlike route functions, all queries are sent on `/rel`, and all of them MUST be `POST`.

When querying resources they're not allowed to access in the database, the status will be `401`. A unknown relation will result in `400`, as `/rel` will never be 404 itself.

`/rel` only returns JSON, even when it replies an error.

For a query to be bidirectional, there is a notion of writability of a column ; a column is said to be writable if and only if it appears exactly once in the select expression and is not transformed by anything other than coalescing operators. Columns are tracked and are writable even if they appear in sub-objects.

A relation's rows are writable iff the columns of its identity target (primary key by default, or whatever on_conflict explicitly designates as the  conflict-resolution unique constraint) are present and writable exactly *once* in the select output.

If a child query disables writability for its own table, it disables it for the whole query, unless it was *explicitely* set to readonly. A user attempting a write on such a query receives an error indicating the offending relation.

(question for claude : should distinct really turn off writing ? it seems to me that if primary keys/unique are returned, then this should be writable and that the contract for writability should be just that. please advise)

Group by is intentionally disabled ; too much abuse potential and it anyways disables writing back entirely on the relation. So do window functions ; these should be done inside views. Rel is about selecting data to write it back (mostly,) the rest can be done in views.

To avoid paying for parsing and preparing statements all the time, rel offers a "well-known" queries mechanism that are read on server startup or after reloading a schema. They are named and make use of the `["$param", ...]` expresion which are transformed into prepared statement param. Another advantage of well-known queries is that they're also exported by rel's typescript/javascript export and typed appropriately.

## Implementation details

* Use github.com/bytedance/sonic
* Do not use struct tags ; JSON must be parsed using .Get and other iterative methods for performance
* Send JSON to postgres, (ab)use json(b)_populate_record and json(b)_populate_recordset
* Use COPY to send the JSON to TEMP tables in postgres

## Transactions

A query or several queries (in one HTTP request) run in a single transaction ; any error stops and rollbacks everything.

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

1. rel assigns every node an index value that will be used in the write query.
  - For each node, real also introspects the select expression to determine where to find the columns of the relation ; it creates an "extractor" that will be able to reconstitute a row from the given JSON (and appends it to the big array mentioned afterwards). This is also where it verifies whether it has enough to perform writes on the table and controls whether it is intended to be readonly or not.
  - Nodes found to be readonly are not processed further (for writing) and are ignored from here on out.
  - For each node and once the columns are known, it will create the corresponding `INSERT` / `UPDATE` / `DELETE` statement that will have to be executed.

2. denormalize the input ; walk the extractors alongside given data and create one big flat JSON array that will contain all the data to be inserted to the server, using the extractors previously created.

  The temporary table that will house them will be like create temp table _data ( `__node_id` int, `__row_id` int, `__parent_id` int, `data` jsonb, `keys` jsonb ), where `__node_id` is the node index in the query, `__row_id` is an absolute row counter and `__parent_id` is the row_id of the parent node containing the current object. This temporary table should exist for all connections of a pool and be properly truncated whenever a query ends. (I'm torn on indexing this table ; maybe the indices on node_id and row_id could be created once COPY is done if there are many rows in this table - sometimes seq scanning is faster. maybe a configuration option like `query.tempindexthreshold` ?)

  keys will have null initially, but will be populated using the RETURNING clause when doing the DML statement on a given node_id - and will be so _only_ with the needed columns and no more. (I don't want the `data` to be touched ; merging is probably more expensive than just creating the new keys object.)

  We want __node_id to know which objects are to be used by the DML statement of a given node, and we need row_id <-> parent_id to join the temp table on itself in the DML of dependent tables.
  
3. as data may be inserted/merged that depends on rows not existing yet, these have to be inserted first. the following occurs recursively :

  1. walk the outgoing relationship
  2. perform data modification for the node (insert, update), joining the temp table as necessary to fetch the foreign values that may have been updated thanks to __parent_id, and store the result of the columns that need to be accessed into keys (thanks to the RETURNING clause) for other nodes.
  3. walk the incoming relationships

4. Perform the DELETE operations, starting with the leave nodes of the query tree in .
  
Note: A relation may self-join : in this case, there are several, distinct nodes. (eg: user that has a manager that is given -> manager is inserted first, then the user. manager is in a subquery node and was resolved as outgoing. recursion stops, because the node with the manager did not do other joins.)

### Insertion / Updates

The default expressions and table column shapes are KNOWN prior to running the algorith ; the database is introspected at start and on migration reload (or manually by the user.) The pg_ tables must _not_ be used in those queries.

Insert and update statements should only include the columns they intend to modify - those that were found in the exploratory phase.

They will use a rehydrated row from `jsonb_populate_record` that they will re-explode column by column.

Most of the time, they will just use the column as is, but when using a column that is a FK to another relation (to a parent, or to an outgoing relation), OR when the JSON does not specify a column that has a default value OR when the JSON has a `null` value for a `NOT NULL` column that has a default value, then we use this value instead.

This looks kind of like that (please correct if you can, but this is the general structure, it's not supposed to compile)

```sql

insert into the_temp_table(__row_id, keys)
(select __node_id, keys from
(insert into target_relation (... impacted columns ...) 
select
  -- for each column, do something like that
   
  case 
    when [column that targets something that may have been set in a previous DML]
      then par.keys->'colname'
    when [no key 'colname' in tmp.data or obj.colname is null in a non null col and colname has a default (which we know from db introspection)]
      then default_value()
    else obj.colname
  end ,

  -- sometimes, columns are simpler ; no defaults, nothing particular, could be null or whatever, so just put it. only 
  obj.colname
  
  -- ...
from the_temp_table tmp
-- inflate our object
join lateral jsonb_populate_record(null::target_relation, tmp.data) as obj on true
-- the following is not always here but, say, when this is a subquery that has a FK to the parent
inner join the_temp_table par on par.__row_id = tmp.__parent_id
where tmp.__node_id = /* the node we're doing now */
returning tmp.__row_id, jsonb_build_object(
  ...pk/unique columns...
) as keys) dml
) on conflict (__row_id) do update set keys = excluded.keys
```

(I wrote it as an insert do update, yet it's only to do an update. maybe a more canonical form would be better but I'm under the maybe mistaken impression that this would be "faster")

### Note about merges

We could use the native Postgres MERGE statement, but it's recent and fairly slow. Keeping to INSERT / UPDATE / DELETE ensures speed and compatibility, as well as simplicity.

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

## Response Shape

See `./query.ts` for the JSON query shape.

---
Don't take into account what follows unless explicitely asked to
---

## Writing Algorithm — draft write-up (Claude), for review

*Everything below is my current understanding after our discussion, offered as a checkpoint to accept, edit, or discard piece by piece — not a replacement for the sections above.*

### The shape of a write

1. **Index assignment.** Every node in the query tree gets a `__node_id` by tree position, not by table — a self-join produces two distinct nodes for the same table. Readonly nodes are excluded entirely from what follows.
2. **Denormalization.** The whole payload is walked into one flat `_data(__node_id, __row_id, __parent_id, data jsonb, keys jsonb)` temp table, loaded via `COPY` for speed. `keys` starts `NULL` and is filled in as each node's rows are actually written (via `RETURNING`), so descendant nodes can pick up foreign values that didn't exist before this transaction.
3. **Phase 1 — inserts/updates/merges, in dependency order, recursively.** For each node: first recurse into its *outgoing* relationships (the things it depends on — writing them first is what makes their generated keys available), then write the node itself (joining back into `_data` to pull any outgoing dependency's freshly-`RETURNING`'d key via `__parent_id`), then recurse into its *incoming* relationships (things that depend on it).
4. **Phase 2 — deletes, deferred until phase 1 has finished for the entire tree.** Every node with a delete component (`merge`, `merge-new`, `merge-update`, `deleteonly`) has its "rows not in the payload" delete queued rather than executed inline as part of step 3. This is the one change from the "Implementation" section above: running every delete only after every insert/update/merge across the *whole* tree has landed means a row being reassigned from one parent to another (present in the payload under its new parent, absent under its old one) has already been re-pointed by the time its old parent's stale-row delete — and any cascade it triggers — actually runs. Cascade firing on a genuinely-abandoned row at that point is correct and expected, not a bug; it just never gets the chance to catch a row that was only ever mid-transition. Deletes can run in any order relative to each other in this phase — one that finds nothing, because cascade or another delete already removed the row, is a harmless no-op.

### Default value assignment — resolved direction, one refinement still open

This closes the gap I flagged: `jsonb_populate_record(NULL::schema.table, json) AS obj` builds the base row, then re-exploding it column by column with `CASE WHEN ... THEN <spliced default expression> ELSE obj.col END` sidesteps the restriction I ran into — it splices the actual default *expression text* (`now()`, `nextval('seq')`, a literal, whatever `pg_attrdef` says it is), not Postgres's bare `DEFAULT` pseudo-value, so it's a normal scalar expression and isn't limited to an `INSERT ... VALUES` position. This is exactly the "pull default-expression text from the cached schema and splice it in per column" mechanism I couldn't find a cleaner alternative to — and it's not hypothetical, it's what legacy's `json_flat.pat` already does (`col.Default != nil && col.NotNull` → `CASE WHEN obj."col" IS NULL THEN <default> ELSE obj."col" END`), so it's a proven pattern, not a new risk.

One refinement worth pinning down: legacy's condition is specifically `col.Default != nil && col.NotNull` — it only substitutes the default when the column is *also* `NOT NULL`. That's not an oversight, it's necessary: `obj.col IS NULL` can't distinguish "key absent from the payload" from "key present with an explicit `null`," since both collapse to `NULL` once they've passed through `jsonb_populate_record`. For a `NOT NULL` column that's fine — an explicit `null` would violate the constraint anyway, so treating it the same as "absent, use the default" is the only sensible outcome either way. For a *nullable* column with a default, though, those two cases are genuinely different and worth keeping apart (this is exactly what the Warnings section above already calls for) — "absent → use the default" but "explicit `null` → actually set it to `NULL`." Getting that right needs the presence test against the *raw* JSON (`jt.json ? 'col'`), not against `obj.col`, since only the raw JSON still has the distinction available. Worth deciding explicitly: does rel extend legacy's behavior to also default-fill nullable columns when the key is absent (needing the raw-JSON test in addition to the `IS NULL` test), or match legacy as-is and leave nullable-with-default columns falling straight through to `obj.col`?

On `CASE` vs `COALESCE`: `CASE` is definitely the right, zero-risk choice here since it's exactly what legacy already runs in production — no reason to introduce any uncertainty by deviating from a proven pattern. Worth being precise about the mechanism, though, since I don't want to leave a rationale in the spec I'm not fully certain of: Postgres's own documentation states `COALESCE` short-circuits identically to `CASE` — "arguments to the right of the first non-null argument are not evaluated" — so at the level of a single flat expression, both should skip a redundant `nextval()` call the same way. The more solid guarantee against an extra sequence increment isn't `CASE` vs `COALESCE` as such, it's making sure the default expression is only ever written into the query in *one place* per row — which the temp-table/`RETURNING`-into-`keys` structure already gives us, independent of which conditional construct wraps it. `CASE` staying the standard is still the right call; just flagging that the "unneeded increments" risk is more precisely about avoiding duplicate evaluation sites in the generated SQL than about `CASE` having some capability `COALESCE` lacks.

### Resolved from earlier discussion, noted here for the record

- **Cyclic/self-referential FKs**: resolved by node-identity-by-tree-position (see the self-join example above) — the recursion walks the query tree, which is acyclic by construction, not the schema's general FK graph, so this was never actually at risk of infinite recursion.
- **Insert-time unique collisions during merge**: not a delete-ordering problem — solved by choosing `upsert` where appropriate.
- **Cascade as a hazard in general**: retracted. It's a legitimate, deliberate schema-level ownership decision, not something rel's write algorithm should second-guess or discourage.
