# Pending actions

Open items surfaced while writing the nested-write example in
`docs/content/example-database/example-queries.md`. All fixed, code and tests included.

## Done : upsert on a non-PK `on_conflict` could corrupt or reject the primary key

`query/write_dml.go`'s `runUpsert` generated `on conflict (<on_conflict cols>) do update set
id = excluded.id` whenever its `DO UPDATE SET` list ended up including the primary key —
`excluded.id` is a phantom pre-insert value (a fresh `nextval()` for a generated column), never
the matched row's real id. On a `GENERATED ALWAYS AS IDENTITY` column Postgres rejects the
statement outright (`column "id" can only be updated to DEFAULT`) ; on a plain `serial` column
it silently reassigns the row's id on every matching write instead of erroring.

Fixed : the primary key is now always excluded from `DO UPDATE SET`, regardless of what
`on_conflict` names. When that leaves nothing to update, the no-op `DO UPDATE` (needed so
`RETURNING` still sees the matched row) now self-references some other real column through the
target table's own name instead of `excluded.*` ; a table whose only column is its own primary
key reports a compile error instead of emitting broken SQL. Regression tests :
`TestExecuteWrite_UpsertNonPKOnConflictPreservesRealKey`,
`TestExecuteWrite_UpsertDefaultPKConflictSelfReferencesOtherColumn`,
`TestExecuteWrite_UpsertDefaultPKConflictWithNoOtherColumn` (`query/write_test.go`).

## Done : `hotel.properties.chain_id` had no index

`test/hotel/schema.sql` never indexed `properties.chain_id`, so any *write* nested under `chains`
(`chains → properties`) failed with `JOIN_MISSING_INDEX`, even a plain insert that touched no
existing row — despite `chains.md` describing `chains → properties → rooms` as the hierarchy
every join/nested-write example in the docs builds on.

Fixed : `create index properties_chain_id_idx on hotel.properties (chain_id);` added to
`test/hotel/schema.sql`. `example-queries.md`'s nested-write example now nests three levels
(chain → property → room types) instead of two, verified live, three runs in a row.

While rebuilding that example, `writing.md`'s `write_mode` table turned out to have a real
inaccuracy of its own : it described `merge-new` as "insert new only, ignore rows that already
exist", with no mention of deleting anything — but the engine's own `hasDeleteComponent`
(`query/write.go`) always treats `merge-new` (and `merge-update`) as delete-bearing, matching the
prose paragraph right below that same table, which does say so. Confirmed live : `merge-new` on
`properties` deleted sibling rows and raised a foreign key violation the table's description
said couldn't happen. The table now names the delete component on every row that has one ;
`insert` is called out as the only mode in the table with none.

## Done : a failing write's error was never logged anywhere

Debug level already logged the SQL text going *out* (`server/query_logging.go`'s `loggingQuerier`
wraps every `Exec`/`Query`/`CopyFrom` a write issues) — that's how the upsert bug above got
diagnosed, by reading the logged SQL and re-running it by hand in `psql`. It never logged what
came back : neither `loggingQuerier` nor `specs/logging.md ## Access logging`'s one `"request"`
line per request captured the returned `error`. A failed write's Postgres error text — the actual
reason for a `500` — wasn't in the logs at any level ; the client-facing response is deliberately
generic under `cfg.Dev == false` (`server/response.go`'s `writeError`), independently of logging.

Fixed : `specs/logging.md ## Error logging` — `writeError` (`server/response.go`) and
`writeErrorForPgErr` (`route/response.go`) now each log one `error`-level `"request failed"` line
for every `5xx` they write, in full, regardless of what the client response itself redacts.
`logging.Error(err) []any` (`logging/logging.go`) is the new helper `specs/logging.md`'s
`## Error integration with samber/oops` had left as "exact shape TBD" — it flattens an `oops`
error's `.With(...)` context into sibling attributes instead of a nested group, so
`logging.filter`/`logging.exclude` can still match on them. Tests :
`TestError_FlattensOopsContextAsSiblingAttrs`, `TestError_NilIsNil` (`logging/logging_test.go`),
`TestWriteError_LogsServerErrorInFull` (`server/error_logging_test.go`).

This fix paid for itself twice more while rebuilding the docs example below : it's what surfaced
`description_search` (a generated column) breaking any write that selects `["own"]`/`["full"]`
on `properties`, and `"select": "*"` never having been valid grammar anywhere in the docs — both
would previously have shown only a bare `"internal error"`.

## Done : root `data` — object or array, and stray `"select": "*"`

`writing.md`, `complex-query.md`'s `stats` example, and both `well-known-queries.md` examples
showed a bare *object* as `data` at the query root. Sent verbatim to a running server, every one
was rejected : `write: payload must be an array of root rows: unsupported type`.
`getting-started/index.md`'s own example used an array at the root and worked.

Fixed as docs-only, per steer : a query's root (`Relation` or `WellKnownQuery` alike) always
reads back as an array, so `data` at the root is always an array too — every object-form example
above now wraps its row(s) in `[...]`, and `writing.md`'s prose states the object/array split
directly (array at the root and any incoming join, single object only at an outgoing join)
instead of implying the root could be either.

While fixing `complex-query.md`'s `stats` example, `"select": "*"` (used there and in
`functions.md`) turned out to never have been valid grammar at all — confirmed live :
`sql: "*" is not a value-position expression`, on a plain read, no write involved.
`selecting.md` only ever documented `["own"]`/`["full"]`/an explicit object ; every `"*"` in the
docs is now one of those instead. The `stats` example's write additionally needed
`write_mode: "upsert"` (the default root `insert` can't match an existing row at all, contrary to
what its own expected response showed) and a real fixture id for the room type it reprices — both
now fixed and verified live.

## Done : `route/response.go`'s `writePlainError` couldn't log its error

`writeErrorForPgErr` (fixed above) was one specific caller of `writePlainError` — the general
plain-text error writer used throughout `route/handler.go`, `route/middleware.go`,
`route/upload_handler.go`, `route/requestbody.go`, and `route/encode.go`. Its signature took a
plain message string, not an `error` : most call sites (`"acquiring connection"`, `"starting
transaction"`, `"setting jwt claims"`, ...) already reduced the underlying error to a static
string before calling it, so there was no `error` left for it to log even after `writePlainError`
itself was taught to.

Fixed : `writePlainError` itself is unchanged (still just writes the response body from a plain
message string) ; a new sibling, `writeServerError(ctx, w, status, code, message, err)`
(`route/response.go`), writes that identical response and additionally logs `err` in full, the
same way `writeErrorForPgErr` already does. Every 5xx call site across those five files that had
a real `err` in scope now calls it instead of `writePlainError` directly — roughly twenty sites.
A handful of `writePlainError` calls are left untouched on purpose : the few with no underlying
`error` at all (`errcode.NoRoleConfigured`'s "no role configured" state, an anonymous-access
rejection), and `route/templates.go`/some of `route/upload_handler.go`'s already log via their
own `rlog.Error(...)` call immediately above, under the `route` module tag. Tests :
`TestWriteServerError_LogsInFull`, `TestWriteErrorForPgErr_UnclassifiedLogsInFull`
(`route/error_logging_test.go`).

## Done : writing `["own"]`/`["full"]` on a relation with a stored generated column always failed

`hotel.properties.description_search` is `generated always as (...) stored` — a real, physical
column, unlike a computed field (which is never a write target at all, per [Writability
rules](../docs/content/query-language/writing.md#writability-rules)). `select: ["own"]` and
`["full"]` (`## Column shapes`, `selecting.md`) both pulled in every physical column
indiscriminately, generated ones included, so either one on a write against `properties` always
failed : `ERROR: cannot insert a non-DEFAULT value into column "description_search"` — confirmed
live, both on `insert` and `upsert`.

Fixed, the same way a computed field is already excluded : `recordOwnColumns` (`query/shape.go`)
now skips any column with `IsGenerated` set (`pg.Column`, already populated by introspection —
the `flagged_columns` fixture in `pg/testdata/schema.sql` already exercised the flag itself, just
not this write path) when expanding `["own"]`/`["full"]` into writable columns. Naming a
generated column explicitly in an object `select` is untouched — Postgres still rejects that
write, same as before ; only the implicit, own/full-driven inclusion is now skipped. Regression
test : `TestExecuteWrite_OwnFullSkipsGeneratedColumn` (`query/write_test.go`).
>> do it like computed fields
