# Test fixtures

This directory holds rel's test fixtures — not `go test` files themselves (those live
next to the code they test, e.g. `pg/info_test.go`), but the shared database schema and
data used to exercise rel's query engine against something more demanding than a
handful of flat tables.

## Why a hotel/booking domain

Chosen deliberately over the more obvious "movie database" : the goal was a schema deep
enough in relationships, and varied enough in Postgres types, to actually stress the
query engine — composite-typed columns, ranges (and an exclusion constraint), full text
search, arrays, enums vs. lookup tables, a UUID primary key alongside identity ones, and
a self-referencing table, without contriving any of it. See the design discussion this
schema came out of for the full reasoning ; the short version is that a hotel/booking
model motivates all of that naturally, where a movie database would have needed several
of those forced in.

## Structure

```
test/
  hotel/
    schema.sql            the `hotel` schema, the btree_gist extension, every table/function
```

A single plain SQL script, applied via testcontainers' `postgres.WithInitScripts` — rel
doesn't embed a migration tool itself (`specs/reload.md`), so this fixture only ever needed
a schema to exist, not to exercise any particular tool's own migration mechanics. See "A
dmut quirk found while writing this" below for how it was originally authored.

## Running

```sh
just test      # go test ./... — every package, including the testcontainer-backed ones
```

`query_bench` applies `test/hotel/schema.sql` itself, in its own `TestMain` — nothing to
run by hand first.

### Manual testing : a database that stays up

`go test` is throwaway — nothing to connect to afterwards. For poking around with `psql`,
a GUI client, or rel itself once it runs, use the `justfile` at the repo root instead : it
manages a **long-lived** dev database, separate from the ephemeral containers `go test`
creates and tears down for itself.

```sh
just test-db-fresh   # tear down, bring up a fresh container, migrate, seed — left running
just test-db-uri     # print its connection string (port is assigned by docker, looked up on demand)
just test-db-psql    # open a psql shell into it
just test-db-down    # stop and remove it ; safe to call even if it isn't up
```

`test-db-up`/`test-db-migrate`/`test-db-seed` also exist individually if you want to seed without a full
reset. There is deliberately no fixed port : `test-db-up` lets docker assign one, so this never
collides with anything else already running — everything else looks it up via `test-db-uri`
rather than assuming one.

## Tables

| Table | Purpose | Worth noting |
|---|---|---|
| `chains` | Hotel group | Plain lookup |
| `properties` | A single hotel | `location point`, `description_search tsvector` (generated, GIN-indexed) |
| `room_types` | Room category within a property | base price, capacity |
| `rooms` | Physical room | `status` enum, `features text[]` |
| `amenities` / `property_amenities` | Amenity catalog + many-to-many | Deliberately parallel to `rooms.features` — same kind of fact, modeled two different ways |
| `loyalty_tiers` | Guest loyalty tier | Lookup table, not an enum — deliberate contrast with the enum columns elsewhere |
| `guests` | A person who can book | `billing_address` is a composite type (`hotel.address`), not a join |
| `payment_methods` | Saved payment method | `card` is a composite type (`hotel.card_summary`) ; never stores a real card number |
| `bookings` | A reservation | UUID primary key ; `stay tstzrange` ; `EXCLUDE USING gist` so no double-booking a room |
| `booking_guests` | Many-to-many | Every guest on a stay, beyond the primary booker |
| `payments` | A charge/refund | UUID primary key, `numeric` + `char(3)` currency |
| `reviews` | Guest review | Second, independent FTS column (`comment_search`) |
| `rate_plans` | Property rate/cancellation policy | The one `interval`-typed column (`cancellation_window`) |
| `staff` | Property staff | Self-referencing (`manager_id`) — the fixture's instance of `query-engine.md`'s own self-join example |

## Functions

**Computed columns** (row-type-taking, usable as `alias.func_name` per `query.ts`) :

| Function | Computes |
|---|---|
| `guest_full_name(guest)` | Display name — pure function of the row, `IMMUTABLE` |
| `booking_nights(booking)` | Length of stay — pure function of the row, `IMMUTABLE` |
| `booking_total_paid(booking)` | Sum of completed payments — reads another table, `STABLE` |
| `property_average_rating(property)` | Average review rating — aggregate over an incoming relation |
| `room_effective_rate(room)` | Nightly rate via `room_type_id` — one-hop lookup |

**Test-purpose** (exercising introspection/argument-mode/return-shape cases rather than being "useful") :

| Function | Exercises |
|---|---|
| `split_name(full_name, out first_name, out last_name)` | `OUT`-mode arguments |
| `concat_features(sep, variadic parts)` | `VARIADIC` mode |
| `search_properties(query)` | `SETOF` / table-valued — a function-*relation*, not a computed column ; searches `properties.description_search` |
| `booking_stats(property_id)` | `RETURNS TABLE(...)` — a `record`-typed return with `OUT`-mode pseudo-args, genuinely different from a named composite type's `Type.IsComposite()`/`Type.Relation` resolution |
| `rooms_available(property_id, on_date default current_date)` | A default argument value — `FunctionArgument` doesn't currently capture these at all |

`booking_stats` and `rooms_available` both have a parameter named the same as a column
they filter on (`property_id`) — resolved by qualifying with the function's own name
(`booking_stats.property_id`), the standard SQL/PL technique for this exact collision.

## A dmut quirk found while writing this

This schema was originally authored as a set of `github.com/ceymard/dmut/v2` migration
files, one per table/function, before rel decoupled from dmut (`specs/reload.md`) and this
fixture was converted to the flat `test/hotel/schema.sql` above. While it was still
dmut-based, `dmut`'s auto-down for `CREATE FUNCTION` was found to incorrectly include
`OUT`-mode parameters in the generated `DROP FUNCTION` signature — Postgres's actual
function signature for `DROP` purposes only includes `IN`/`INOUT`/`VARIADIC` parameters.
Verified directly (`dmut test` failed with `function ... does not exist` on `split_name`'s
down step until this was worked around with an explicit `up`/`down` pair). Noted here as a
still-relevant heads-up for anyone using dmut directly (e.g. via `reload.cmd`) — this is a
bug in `dmut` itself (`github.com/ceymard/dmut/v2`, a separate repo), not something rel
fixes or works around on your behalf.

## Fake data

`test/seed/main.go` — a Go program (not SQL), run after the schema is in place :

```sh
psql <postgres-uri> -v ON_ERROR_STOP=1 -f test/hotel/schema.sql
go run ./test/seed <postgres-uri>          # or set DATABASE_URL instead of passing it
```

Uses `github.com/brianvoe/gofakeit/v7` for realistic names/addresses/text, connecting via
the same `pgx` already used elsewhere in this repo. A fixed seed (`const seed = 42`) makes
every run reproducible — the data looks random but is identical from one run to the next,
not regenerated differently each time.

Mostly bulk-generated (3 chains, ~13 properties, ~195 rooms, 200 guests, 500 bookings,
...), plus a handful of hand-picked "named" rows for scenarios worth being able to
reference directly in future tests : a guest with no bookings at all
(`never.booked@example.test`), a property with no reviews ("The Unreviewed Inn"), and a
three-level `staff.manager_id` chain per property (GM → assistant manager → line staff).

Two things worth knowing, found while building this rather than assumed :

- **Composite-typed columns** (`billing_address`, `card`) are inserted via
  `ROW($1, $2, ...)::hotel.address` directly in the SQL text — no special pgx type
  registration needed.
- **The room-booking exclusion constraint is respected by construction, not by retrying
  on conflict** : the seeder tracks each room's already-assigned date ranges in memory
  (`room.occupied`) and only ever picks a non-overlapping one. Verified directly against
  the seeded database (`select ... from bookings a join bookings b on a.room_id = b.room_id
  and a.stay && b.stay` returns zero rows) rather than assumed from the absence of an
  insert error.
- **Description/review text uses `gofakeit.Paragraph()`/`Sentence()`, not
  `LoremIpsumParagraph()`/`LoremIpsumSentence()`** — the Lorem Ipsum variants generate
  actual Latin placeholder text, which has no real English vocabulary for full text search
  to match against. First pass used the Lorem Ipsum functions and `search_properties`
  correctly returned nothing for any real search term ; switching fixed it, confirmed by
  searching for a deliberately-included city name and getting the expected single match.
