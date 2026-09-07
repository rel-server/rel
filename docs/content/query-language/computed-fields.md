---
icon: material/function-variant
---

# Computed fields

Postgres lets a function stand in for a column: any function taking a table's own row type as
its sole argument can be called as `alias.func_name` (or `func_name(alias)`) in ordinary SQL,
exactly like a real column. rel discovers these functions at introspection time and makes them
usable by bare name — in `select`, `where`, `order_by`, anywhere a real column would go.

## Eligibility

A function is registered as a computed field on relation `R` when:

- it lives in `R`'s own schema;
- it's callable with exactly one argument — every other parameter, if it has any, must have a
  default;
- that one argument's type is `R`'s own composite row type;
- it isn't a Postgres aggregate, window function, or procedure — a plain function only.

```sql
create function hotel.booking_nights(booking hotel.bookings) returns integer
language sql as $$
  select ceil(extract(epoch from upper(booking.stay) - lower(booking.stay)) / 86400)::integer;
$$;
```

[Database reference](database-reference.md) lists every computed field the hotel schema
already defines this way (`hotel.booking_nights`, `hotel.booking_total_paid`,
`hotel.guest_full_name`, `hotel.property_average_rating`).

A function whose name collides with one of `R`'s own physical columns is never registered as a
computed field at all — the real column wins. That collision is a schema-authoring mistake to
fix at the source (rename one or the other), not something a query can work around.

## Using one

Reference a computed field by its bare name, exactly like a column — no special syntax needed:

```json
{
  "select": { "name": "name", "nights": "booking_nights" },
  "where": ["=", "booking_nights", 3]
}
```

This also works through a `.` hop into a joined, to-one relation's own computed field, the same
way a real column does:

```json
{ "manager_name": [".", "manager", "guest_full_name"] }
```

A computed field is never selected by default: `own`/`full` never pull one in automatically,
even one already exposed in Postgres as a function over the row type. Naming a computed field
in `own_except`/`full_except` is an error — it was never included in the first place, so there's
nothing to except.

A cross-schema function — eligible in every other respect, but living in a different schema
than the relation it targets — is never discoverable by bare name. Reach it with `call`
instead, passing the relation's own declared `alias` as the row argument:

```json
{
  "alias": "b",
  "select": { "surcharge": ["call", {"schema": "billing", "name": "late_fee"}, "b"] }
}
```

`call`'s function name is either a bare, unqualified string (resolved via the search path) or
an explicit `{"schema": ..., "name": ...}` object, and is checked against the same function
allowlist a computed field's own function is — see [Configuration reference](../configuration/reference.md).

## What a computed field returns

- **A scalar return type** (`integer`, `text`, `numeric`, ...) produces a plain value, the
  same as selecting an ordinary column.
- **A return type matching another relation's own row type, or `setof` that relation** comes
  back as an object (a single matching row) or an array (`setof`) the same way an embedded join
  would — but unlike a real join, a computed field carries no `select`/`join` of its own: the
  fields that come back are exactly whatever columns the function's own return type declares,
  verbatim. There's no way to trim or nest further at the query level; narrow what a computed
  field exposes inside the SQL function's own definition instead.

```sql
-- illustrative: not part of the hotel-booking fixture
create function hotel.recent_reviews(property hotel.properties)
returns setof hotel.reviews
language sql as $$
  select * from hotel.reviews
  where reviews.property_id = property.id
  order by created_at desc
  limit 5;
$$;
```

```json
{ "recent_reviews": "recent_reviews" }
```

comes back as an array of review rows, shaped however `hotel.reviews`' own columns are —
narrower than a real `join`, which does let you `select` a subset at the query level.

A computed field is never a write target, regardless of whether it's reached by bare name or
via `call` — it isn't a real column to begin with. See [Writing data back](writing.md) for what
makes a column writable.
