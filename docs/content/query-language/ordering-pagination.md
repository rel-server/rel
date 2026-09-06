---
icon: material/arrow-up-down
---

# Ordering and pagination

```json
{
  "order_by": [["desc", "star_rating"], "name"],
  "limit": 10,
  "offset": 20
}
```

`order_by` is a list of terms, applied in order — ties on the first term are broken by the
second, and so on. A bare term (`"name"` above) is a plain column or expression reference,
ascending; the tagged form (`[direction, Expression]`) picks one of:

| Tag | Meaning |
|---|---|
| `asc` | Ascending, nulls last — the default for a bare (untagged) term too. |
| `desc` | Descending, nulls last. |
| `asc-nulls-first` | Ascending, nulls first. |
| `desc-nulls-last` | Descending, nulls last — same as plain `desc`; spelled out for symmetry with `asc-nulls-first`. |

Nulls sort last by default in both directions — plain SQL's own default is nulls-last for
`ASC` but nulls-*first* for `DESC`; rel normalizes `desc` to nulls-last too, so switching a
term's direction never silently moves nulls around as a side effect. `asc-nulls-first` is the
only tag that puts nulls first; there is no `desc-nulls-first`.

`limit`/`offset` are plain integers, applied after `order_by`. Without an `order_by`, which
rows a `limit` keeps — and what `offset` skips past — is whatever order Postgres happens to
produce, not a guarantee; give a query an explicit `order_by` whenever `limit`/`offset` depend
on a specific row surviving the cut.

Inside a joined subquery, `limit`/`offset`/`order_by` all apply **per parent row** — `limit: 3`
on a `room_types` join returns up to 3 room types for *each* property, not 3 total across the
whole result. See [Distinctness](distinctness.md) for `distinct`/`distinct_on`, which follow
the same per-parent-row rule.
