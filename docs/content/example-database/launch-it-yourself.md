---
icon: material/download
---

# Launch it yourself

This brings up the exact database every example in this documentation queries, then runs rel
against it from source. It needs Go and Docker (running, to host the database container)
installed locally — nothing else.

## The quick way

The checked-out repo has a `justfile` that wraps every step below into a few commands:

```sh
git clone https://github.com/rel-server/rel.git
cd rel
just test-db-fresh   # tear down, bring up Postgres, migrate, seed — leaves it running
just test-run        # builds and runs rel against it, on :8080
```

`just test-db-psql` opens a `psql` shell into the same database if you want to look at the
schema or the seeded rows directly; `just test-db-down` stops and removes the container.

## The manual way

The same steps, without `just` — useful if you don't have it installed, or want to see what
each step actually does.

Clone the repo and fetch its dependencies:

```sh
git clone https://github.com/rel-server/rel.git
cd rel
go mod download
```

Start a throwaway Postgres container:

```sh
docker run -d --name rel-test-db \
  -e POSTGRES_USER=hotel -e POSTGRES_PASSWORD=test -e POSTGRES_DB=hotel \
  postgres:16-alpine
```

Apply the schema ([Database reference](reference.md) is the same schema this creates) and seed
it with fake data:

```sh
export DATABASE_URL="postgres://hotel:test@localhost:5432/hotel"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f test/hotel/schema.sql
go run ./test/seed "$DATABASE_URL"
```

The seeder (`test/seed/main.go`) is a fixed-seed Go program, not a SQL script — every run
produces the same data: a handful of chains and properties, ~200 guests, ~500 bookings, and a
few hand-picked rows worth referencing directly (a guest with no bookings at all, a property
with no reviews, a three-level manager chain in `hotel.staff`).

Build and run rel against it — `REL_PG__URI` is the same connection string as `$DATABASE_URL`
above, rel doesn't read `DATABASE_URL` itself:

```sh
REL_PG__URI="$DATABASE_URL" go run ./cmd/rel
```

rel is now serving on `:8080` — see [Getting started](../getting-started.md) for a first query
against it.

## Building a binary instead of `go run`

`go run ./cmd/rel` compiles and runs in one step, rediscarding the binary afterwards — fine for
poking around, wasteful for anything you'll run more than once:

```sh
go build -o rel ./cmd/rel
REL_PG__URI="$DATABASE_URL" ./rel
```

There's no separate `go install`-able module path yet — building from the checked-out repo, as
above, is the only supported source build. See [Docker deployment](../configuration/docker-deployment.md)
if you'd rather run rel from the published image than build it yourself.
