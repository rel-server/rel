# Writing data back

Send `{"query": <Relation>, "data": <payload>}` instead of a bare `Relation` to write. `data`
must be shaped like what `query`'s own `select` would produce — the same object (or array of
objects, for the root or an incoming join) you'd get back from reading, edited in place. rel
denormalizes it back down to one row per relation the query touched and applies each relation's
own `write_mode`:

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

A column is writable only if it appears exactly once in `select` (see [Selecting
fields](selecting.md)), untransformed except by a coalescing operator (`??`, `||?`,
`coalesce`) or `set`/`get-set`; a relation is writable only if its identity columns (the
primary key, or whatever `on_conflict` names) are present and writable that same way. A
computed column (anything reached through [`call`](aggregates.md)) is never a write target —
it isn't a real column to begin with.
