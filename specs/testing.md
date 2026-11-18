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
stay individually small. Cheap to stand up (no seeding pass) — the default for a test that
only needs a couple of known rows to exercise one specific code path.

> Why: keeping fixtures small means a new test is usually satisfied by one or two added lines, not a schema redesign.

### `test/hotel` + `test/seed` — realistic volume

A flat SQL schema (`test/hotel/schema.sql`, `postgres.WithInitScripts`, same mechanism as
`pg/testdata/schema.sql` above) plus a Go seeding program (`test/seed/seed`,
`github.com/brianvoe/gofakeit/v7`, a fixed seed for reproducibility) — see `test/README.md`
for the full "why a hotel/booking domain" reasoning. Slower to stand up (a seed pass
generating hundreds of rows), but it's the only fixture that :

- Has genuine relationship depth and volume : composite types, ranges with an exclusion
  constraint, full-text search, a self-referencing table, hundreds of rows — deep/wide
  enough to stress the query engine in ways a handful of hand-picked rows can't.
- Is what `benchmarks/internal/inprocess/`'s read-path benchmarks build on. `query/write_bench_test.go`'s
  write-path benchmarks do NOT use this tier — they run against the flat `director`/`movie`
  fixture above, via `query/node_resolve_test.go`'s own `TestMain`.

> Why a benchmark uses this tier: it needs realistic shape/volume to mean anything.

## When to reach for which

- Testing one specific behavior in isolation (a new operator, a new resolution rule, a new
  HTTP status mapping) : `pg/testdata/schema.sql`-style, add the minimal fixture the test
  needs.
- Testing something that only manifests at realistic depth/volume, or writing a
  read-path benchmark : `test/hotel` + `test/seed`.

## Request formats

The `relexec` and `server` suites send their JSON request bodies through one helper each (`parseBody`, `newRelPost`), and the `query` write tests their payloads through `jsonPayload`. With `REL_TEST_REQUEST=cbor` the helper sends the CBOR transcoding of the body instead (`cborenc.AppendJSON`), so every request-level test also checks the CBOR path ; `just test` runs both. A new request-level test goes through these helpers, and a test of a value only CBOR can carry (a tag, a byte string) builds its CBOR body itself.

The TypeScript client has the same split. `typescript/trunk/` holds what the client does whatever the codec : `test/tsclient` copies it into every `typescript/combos/<codec>/trunk/` (ignored by git) and typechecks and runs it there against that codec's `database.ts`. It runs once per codec against one database, so it asserts on fixture rows and on rows it names uniquely, and compares a date and time through one form whatever the codec gives. A combo's own test file checks what that codec alone does (the JS type of each value), and `typescript/tests/` what only the default `json,temporal` file does.

## Open

Whether this two-tier split should stay purely a convention (this document) or gain some
enforcement (a lint rule, a naming convention for test files that makes which tier they use
obvious at a glance) is undecided — no evidence yet that the convention alone isn't holding.
