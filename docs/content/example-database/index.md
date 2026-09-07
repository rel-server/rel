---
icon: material/database
---

# Example database

Every example in this documentation queries the same schema: a small hotel-booking database —
chains, properties, rooms, guests, bookings, payments — deep enough in relationships and varied
enough in Postgres types to show what joins, aggregates, and nested writes actually look like
against something more real than a couple of flat tables.

This section is a reference for that schema — [Database reference](reference.md) has the full
entity diagram, and each table listed there gets its own page with its columns, its own slice of
that diagram, and what makes it worth looking at in this fixture specifically.

It's also a real, running database you can point rel at yourself: it ships as this repo's own
dev fixture, so once you have the code checked out, bringing it up locally is one command away
— see [Launch it yourself](launch-it-yourself.md).

## In this section

- **[Database reference](reference.md)** — the full schema, its entity diagram, and the table of
  contents for every table's own page.
- **[Launch it yourself](launch-it-yourself.md)** — checking out the repo, building rel, and
  bringing up this exact database against it.
