# Joining and embedding relations

`join` adds a related relation under an alias, using `on` to say how it connects — the on
object's *keys* are the joined relation's own columns, its *values* are the enclosing query's
columns:

```json
{
  "relation": "properties", "schema": "hotel",
  "join": {
    "room_types": {
      "relation": "room_types", "schema": "hotel",
      "on": { "property_id": "id" }
    }
  }
}
```

Whether the embed comes back as an array or a single object follows directly from which side
owns the relationship, not from anything you declare: `room_types.property_id` points *at*
`properties.id`, many rows can share one property, so `room_types` comes back as an array — an
**incoming** relation. Join the other way — `properties.chain_id` pointing at `chains.id` — and
you get one object, never an array: an **outgoing** relation. This distinction also picks each
relation's default write mode (see [Writing data back](writing.md)) and decides where
[`agg`](aggregates.md) can be used. [Database reference](database-reference.md) has the full
diagram of which foreign key points which way across the hotel schema.

A join's `on` columns must be backed by a foreign key, or by a unique constraint on whichever
side is the "one" side, and the joined side's own `on` columns must be indexed — rel refuses
to compile a query that would silently compile into a per-parent-row sequential scan.
