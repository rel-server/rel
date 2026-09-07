# Postgres connection precedence

## Current issue

`pg.uri`, when set, is independent of `pg.host`/`pg.port`/`pg.database` — the granular fields are neither derived from it nor checked against it.

## Solution

When `pg.uri` is set, it takes precedence and populates `pg.host`/`pg.port`/`pg.database`.

Setting `pg.host`, `pg.port`, or `pg.database` alongside `pg.uri` is a configuration error, not a silently-ignored value.
