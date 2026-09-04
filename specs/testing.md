# Testing

`AGENTS.md` mandates testcontainers-backed regression/integration tests over a real Postgres
— this document is where the actual conventions that follow from that live, rather than
scattered per-package doc comments each reinventing them.

## Fixture conventions

Two fixture conventions are in active use today, for different jobs — not competing, not a
transitional state where one is meant to replace the other.

### `pg/testdata/schema.sql` — flat, fast, one behavior at a time

A plain SQL init script (`postgres.WithInitScripts`), applied directly by testcontainers with
no migration engine involved. This is where most packages' fast, single-behavior regression
tests live — `query/`, `pg/`, `route/`, `static/`, `server/`, `boot/` all build their own
`TestMain` against it (or a package-local variant of it). Its `director`/`movie` relations,
plus a purpose-built fixture table/function added per test as new behavior needs covering,
stay individually small. Cheap to stand up (no `dmut` apply, no seeding pass) — the default
for a test that only needs a couple of known rows to exercise one specific code path.

> Why: keeping fixtures small means a new test is usually satisfied by one or two added lines, not a schema redesign.

### `test/dmut` + `test/seed` — realistic volume, dmut integration coverage

A `dmut`-migration-based schema (`test/dmut/*.yml`) plus a Go seeding program
(`test/seed/seed`, `github.com/brianvoe/gofakeit/v7`, a fixed seed for reproducibility) —
see `test/README.md` for the full "why a hotel/booking domain" reasoning. Slower to stand up
(a real `dmut apply` plus a seed pass generating hundreds of rows), but it's the only fixture
that :

- Exercises `dmut` itself as a real integration point — a regression in `dmut`'s own
  behavior, or in how rel drives it (`dmut/run.go`), shows up here, not just in `dmut`'s own
  separate test suite.
- Has genuine relationship depth and volume : composite types, ranges with an exclusion
  constraint, full-text search, a self-referencing table, hundreds of rows — deep/wide
  enough to stress the query engine in ways a handful of hand-picked rows can't.
- Is what `query_bench/`'s read-path benchmarks and `query/write_bench_test.go`'s
  write-path benchmarks both build on.

> Why a benchmark uses this tier: it needs realistic shape/volume to mean anything.

## When to reach for which

- Testing one specific behavior in isolation (a new operator, a new resolution rule, a new
  HTTP status mapping) : `pg/testdata/schema.sql`-style, add the minimal fixture the test
  needs.
- Testing something that only manifests at realistic depth/volume, or that specifically
  needs `dmut` in the loop, or writing a benchmark : `test/dmut` + `test/seed`.

## Open

Whether this two-tier split should stay purely a convention (this document) or gain some
enforcement (a lint rule, a naming convention for test files that makes which tier they use
obvious at a glance) is undecided — no evidence yet that the convention alone isn't holding.
