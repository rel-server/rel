---
icon: material/set-none
---

# Distinctness

`distinct` and `distinct_on` follow directly from SQL's own `DISTINCT` and `DISTINCT ON` — rel
doesn't reinterpret either, it just compiles them straight through.

## `distinct`

```json
{ "distinct": true }
```

A plain boolean. Collapses fully-duplicate rows out of the result, comparing every selected
field. If both `distinct` and `distinct_on` are given on the same node, `distinct` wins and
`distinct_on` is silently ignored.

## `distinct_on`

```json
{
  "distinct_on": ["chain_id"],
  "order_by": ["chain_id", ["desc", "star_rating"]]
}
```

A list of expressions. Keeps one row per distinct combination of those expressions — which
row survives is whichever one sorts first under `order_by`. The query above keeps each hotel
chain's highest-rated property: one row per `chain_id`, and among a chain's properties, the
one with the greatest `star_rating`.

`distinct_on`'s expressions must be the leading terms of `order_by`, in the same order —
that's how "which row wins" gets defined at all. This isn't a rule rel enforces itself;
Postgres rejects the query outright if `distinct_on` isn't a prefix of `order_by`, and if
`order_by` is omitted entirely, Postgres keeps a row per group but which one is unspecified —
usually whichever one it happens to read first, not a meaningful "top" row. Always pair
`distinct_on` with an `order_by` that starts with the same expressions, plus at least one more
term to break ties within a group.

Inside a joined subquery, `distinct`/`distinct_on` apply **per parent row**, the same as
[`limit`/`offset`/`order_by`](ordering-pagination.md) — `distinct_on: ["room_id"]` on a
`bookings` join (rooted at `guests`) keeps one booking per room *within each guest's own
bookings*, not one per room across every guest.
