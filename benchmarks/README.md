# Benchmarks

Compares rel with PostgREST and PostGraphile (v4 and v5) on the same Postgres database and the same data, for reads and writes, over HTTP. The in-process benchmarks of rel's own query compiler live in `internal/inprocess` (run them with `go test ./benchmarks/internal/inprocess -run=^$ -bench=.`).

## Running

```sh
just bench-quick          # small data, 1 repeat, a few minutes of measuring per server
just bench-full           # small, medium and large data, 3 repeats ; takes hours
go run ./benchmarks/cmd/bench -list                        # list the scenarios
go run ./benchmarks/cmd/bench -check                       # run every scenario once per server, no timing
go run ./benchmarks/cmd/bench -scenarios 'read/pk' -servers rel,postgrest
```

Needs docker and a machine with at least 3 CPU cores. Results go to `benchmarks/results/<date>-<profile>/` (`report.md` for people, `raw.json` for everything else). That directory is git-ignored: copy the numbers you want to publish into the docs by hand.

Before a run that matters, set the CPU governor to `performance` and close other heavy programs. The harness pins things to cores but cannot control the rest of your machine.

## What runs

Four servers, each started on its own and stopped before the next one starts, so they never compete:

| Server | Image | Notes |
|---|---|---|
| rel | built from the repository's `Dockerfile` | |
| PostgREST | `postgrest/postgrest:v16.4` | |
| PostGraphile 4 | `postgraphile@4.14.1` | plugins: connection filter, simplify inflector, nested mutations |
| PostGraphile 5 | `postgraphile@5.1.5` | plugins: connection filter, simplify inflection |

All of them connect to one Postgres 16 container (`compose.yaml`) as the same role, with a pool of 20 connections each. No authentication is used anywhere, so the numbers show query and serialisation cost, not token checking. rel and PostgREST still switch to their anonymous role on every request, as they would in production; PostGraphile does not.

The nested-mutations plugin only exists for PostGraphile 4, so the three-level write is native there and emulated (3 round trips) on PostGraphile 5.

## Data

The hotel schema from `test/hotel`, filled by `test/seed` with a fixed seed. Three sizes, chosen with `-sizes`:

| Size | Seed scale | Bookings |
|---|---|---|
| small | 1 | 500 |
| medium | 200 | about 100 000 |
| large | 1000 | about 500 000 |

Rows are inserted one at a time, so building a large database takes a long time. It is built once, kept as a "golden" database in the Postgres volume, and cloned before every run that writes. Use `-rebuild` to build it again.

`db/indexes.sql` adds the foreign-key indexes the schema leaves out, because rel refuses to join on an unindexed column and a real database would have them. Every server benefits equally.

## Scenarios

Each scenario has one request per server. Reads come back as the same rows and fields on every server: before timing, the harness runs the scenario once on each server, reduces the answers to a common shape, and skips the scenario for any server whose answer differs (the report says why). Writes check the database afterwards (row counts) instead.

Each cell of the report says how a server did the work: **native** (one request), **emulated** (the client chains requests, with the number listed) or **unsupported**.

- Reads: primary-key lookup, filter + sort + paginate, boolean filter with `like`, two to-one embeds, to-many embed, deep nested embed, embedded count, full-text search function, array column filter, composite column field, wide unscoped read.
- Writes: insert one, update by primary key, delete by primary key, bulk insert of 100, three-level nested write, upsert.

## How it is measured

- Closed loop: each client sends its next request when the previous answer arrives, at 1, 8, 32 and 64 clients (reads) or 1, 8 and 32 (writes). Under overload this understates tail latency, because a slow server is also sent fewer requests.
- Reads: a warm-up (discarded), then a fixed time window. Writes: a fixed number of operations on a fresh copy of the database, with no warm-up.
- Reported: requests per second, p50/p95/p99 latency, errors, bytes and round trips per operation, and CPU and memory of the server and database containers, sampled with `docker stats`. With several repeats the median run by throughput is shown, with the minimum and maximum.
- CPU pinning: the machine's cores are split between the load generator (a quarter), the database and the server under test. The split is printed in the report.

## Known limits

- The load generator runs on the same machine as everything else.
- PostGraphile 5 hides filters and orderings on unindexed columns by default; the benchmark turns that off (`PgIndexBehaviorsPlugin`) so it does the same work as the others.
- The docker stats CPU numbers are coarse: one sample takes about a second.
