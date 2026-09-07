---
icon: material/pencil
---

# Writing data back

Send `{"query": <Relation>, "data": <payload>}` — a `ComplexQuery` — instead of a bare `Relation`
to write. `data` must be shaped like what `query`'s own `select` would produce — the same object
(or array of objects, for the root or an incoming join) you'd get back from reading, edited in
place. rel denormalizes it back down to one row per relation the query touched and applies each
relation's own `write_mode`:

`ComplexQuery` also carries five flags — `returns`, `count`, `stats`, `query_plan`, `sql`, and
`rollback` — that shape what the response carries beyond the written-back selection, and work on
a plain read too (just omit `data`). See [Complex queries](complex-query.md).

| `write_mode` | Behavior | Default for |
|---|---|---|
| `insert` | insert new rows only | the root relation |
| `upsert` | insert or update, never delete | an outgoing join |
| `merge` | insert new, update matching, delete rows missing from the payload | an incoming join |
| `merge-new` | insert new only, ignore rows that already exist | — |
| `merge-update` | update matching only, ignore new rows | — |
| `update` | update matching rows only, never insert or delete | — |
| `deleteonly` | delete rows missing from the payload, nothing else | — |
| `readonly` | never write this relation (or anything nested under it) | — |

`merge`/`merge-new`/`merge-update`/`deleteonly` all delete rows the payload didn't mention, so
they're only legal on an incoming relation (see [Joining and embedding
relations](joining.md)) — there's no coherent "rows missing from the payload" for an outgoing
one, since the referenced row isn't exclusively owned by the one pointing at it. `on_conflict`
names the constraint (by name, or by its column list) used to detect an existing row on
insert/upsert/merge; it defaults to the relation's primary key. `insert_columns`/
`update_columns` narrow which columns a write actually touches, beyond whatever `select`
already made writable.

## Write order

A request isn't one big statement — it's every relation's own insert/update/upsert/delete, run
in a specific order, all inside one transaction. Knowing that order explains things that would
otherwise look like magic: why a brand-new parent's generated `id` is already available to the
child rows pointing at it, and why a row reassigned from one parent to another survives instead
of being deleted and recreated.

**Phase 1 — every insert/update/upsert, before any delete.** Within one relation:

1. Its **outgoing** joins run first — the relations it points *at* (e.g. `properties.chain_id
   → chains.id`, see [Joining and embedding relations](joining.md)). Their rows are written,
   and whatever identity value they end up with (freshly generated or already known) is
   resolved before this relation's own row is written, since this relation may need it.
2. This relation's own row(s) are written — insert, update, or upsert, per its `write_mode` —
   using whatever values step 1 resolved for any outgoing reference.
3. Its **incoming** joins run last — the relations that point *at* it (e.g.
   `room_types.property_id → properties.id`). They need this relation's own identity value,
   possibly one that was only just generated in step 2, to write correctly.

So writing `properties` nested under `chains`, with `room_types` nested under `properties`
(three levels), runs `chains` first, then `properties`, then `room_types` — outermost
dependency to innermost dependent, regardless of how the JSON happens to be nested. This is
also why a brand-new property can arrive in the same request as its brand-new room types,
before the property has an `id` of its own: `properties` is written first and its newly
generated `id` is threaded into each `room_types` row as it's written next. The same recursion
handles a self-join (a `manager` field on `staff` pointing back at another `staff` row): the
manager row is written first, being an outgoing reference, then the staff row that points at
it.

**Phase 2 — every delete, only after phase 1 has finished for the whole request.** A
`write_mode` with a delete component (`merge`, `merge-new`, `merge-update`, `deleteonly`)
deletes rows that exist under its parent in the database but weren't mentioned in the payload.
These deletes run after every insert/update/upsert across the *entire* query tree, not just
this relation's own branch — and deepest-first: a relation's delete only runs once every
relation nested under it has already had its chance to delete. This mirrors how Postgres itself
enforces foreign keys: deleting a row before whatever still points at it is gone fails with a
foreign key violation.

Deferring every delete this way is also what makes reassignment safe. Move a `room_types` row
from one `properties` parent to another in the same request — present under its new parent's
payload, absent from its old one — and phase 1 has already re-pointed it at the new parent by
the time the old parent's "delete what's missing" delete runs; it survives, rather than being
deleted and needing to be recreated. Two delete-bearing relations reached through separate,
unrelated branches of the same query have no defined order relative to each other — the worst
case if that ever matters is a foreign key violation that rolls back the whole request, never
silent data loss.

## Writability rules

- A **column** is writable only if it appears exactly once in `select` (see [Selecting
  fields](selecting.md)), untransformed except by a coalescing operator (`??`, `||?`,
  `coalesce`) or `set`/`get-set`. A [computed field](computed-fields.md) — whether reached by
  bare name or through `call` — is never a write target: it isn't a real column to begin with.
- A **relation** is writable only if its identity columns (the primary key, or whatever
  `on_conflict` names) are present and writable, by the rule above, exactly once in its own
  `select`.
- A **query** is writable only if every relation in it is writable — root and every joined
  relation, at every depth. One unwritable relation anywhere in the tree makes the *whole*
  write request fail, not just that relation's own piece of it - unless it is explicitely
  `write: "readonly"`.

That last rule is the one worth pausing on, because it's easy to write a query that reads back
fine and then silently isn't writable the way you'd expect. Take this nested write — the intent
is to rename a property and reprice one of its room types:

```json
{
  "query": {
    "relation": "properties", "schema": "hotel",
    "join": {
      "room_types": {
        "relation": "room_types", "schema": "hotel",
        "on": { "property_id": "id" },
        "select": { "name": "name", "base_price": "base_price" }
      }
    },
    "select": { "id": "id", "name": "name", "room_types": "room_types" }
  },
  "data": {
    "id": 1,
    "name": "Marina Bay Grand Hotel",
    "room_types": [{ "name": "Deluxe", "base_price": "219.00" }]
  }
}
```

`room_types.select` never includes `id` — its identity column — so `room_types` isn't writable
under the rule above, even though `properties` itself is and `base_price` is an ordinary,
otherwise-writable column. Because `room_types` is joined (not marked `readonly`), that
unwritability propagates up: the whole request is rejected before anything is written,
`properties.name` included, with a `400` naming the actual offending relation:

```json
{
  "status": "error",
  "code": "WRITE_FORBIDDEN",
  "error": "write: relation \"room_types\" is not writable — its identity columns must appear exactly once in the select output, untransformed and writable"
}
```

The alternative — writing `properties` while silently skipping `room_types` because it
"couldn't" — is exactly what this rule avoids: a request that reads back like it worked, having
actually left part of what you sent untouched, with nothing in the response telling you so. rel
would rather reject the whole request up front, atomically, and name the relation that broke
it, than leave you to notice a missing update later.

If a joined relation is only ever meant to be read, not written — an identity-omitting
projection like the one above, a computed-only shape, or a lookup table you never intend to
edit through this query — mark it `write_mode: "readonly"` explicitly. A `readonly` relation
(and everything nested under it) is exempt from this propagation: it can be as unwritable as it
likes without blocking the rest of the request.
