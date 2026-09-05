# Ordering, distinctness, and pagination

```json
{
  "order_by": [["desc", "star_rating"], "name"],
  "distinct_on": ["chain_id"],
  "limit": 10,
  "offset": 20
}
```

Plain `order_by` entries default to ascending, nulls last, matching SQL; the tagged form
(`["desc", ...]`, `["asc-nulls-first", ...]`, `["desc-nulls-last", ...]`) overrides either.
`distinct`/`distinct_on` follow directly from SQL's own `DISTINCT`/`DISTINCT ON`. Inside a
joined subquery, `limit`/`offset`/`order_by`/`distinct*` all apply **per parent row** — `limit:
3` on a `room_types` join returns up to 3 room types for *each* property, not 3 total across
the whole result.
