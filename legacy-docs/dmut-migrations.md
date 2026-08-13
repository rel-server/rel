# Legacy `dmut` Migration Integration Reference

Source files: `dmut.go`, wiring in `main.go`, `auth.go` (`ReloadLoginFunctions`), `sw/defs.go`
(`SwServer.DmutStatus`), plus the two vendored libraries `github.com/ceymard/dmut` (v1,
`go.mod:8`) and `github.com/ceymard/dmut/v2` (v2, `go.mod:9`). Example migration file:
`test/dmut/index.dmut`. Package `main` (module `sales-way.com/server`), except the two
`dmut`/`dmut/v2` modules which are separate Go modules pulled in as dependencies (source
read from the local module cache at
`~/go/pkg/mod/github.com/ceymard/dmut@v0.3.1` and `~/go/pkg/mod/github.com/ceymard/dmut/v2@v2.0.1`).

## 1. Purpose

`dmut` is a standalone, dependency-graph-based SQL schema migration tool (a separate Go
module/CLI by the same author, vendored as a library here). Unlike sequential
numbered-migration tools, a `dmut` migration ("mutation") is identified by a **name**
and a **content hash**; mutations declare `depends on` / `needs` relationships to form a
DAG. On each run, `dmut` diffs the mutation set stored in the database against the
mutation set found on disk: unchanged mutations are left alone, changed or removed
mutations are "downed" (their auto-generated or explicit `DOWN` SQL is run, recursively
downing dependents first), and new/changed mutations are "upped". This makes migrations
idempotent and re-appliable: the same `index.dmut` file can be run repeatedly and only
the delta is executed. The whole run happens inside a single transaction/savepoint tree,
so a failure anywhere aborts cleanly without partial application. In this legacy server,
`dmut` is the mechanism that creates/maintains the Postgres schema, roles, tables, RLS
policies, grants and stored functions that the rest of the server (PostgREST-like
routing, auth) depends on at runtime.

## 2. Which version is actually used — `dmut.go`

`dmut.go` imports **both** major versions:

```go
import (
    "github.com/ceymard/dmut/mutations"
    mut2 "github.com/ceymard/dmut/v2/mutations"
)
```
(`dmut.go:6-7`)

`tryRunDmut` (`dmut.go:26-56`) actually *uses both*, gated by which marker path exists on
disk, checked in this order:

1. **v2 first**: if `/dmut2` exists **and is a directory**, it is used exclusively via
   `mut2.ReadAndRunMutations(pguri, []string{"/dmut2"}, mut2.MutationRunnerOptions{Commit: true})`
   (`dmut.go:28-39`). On success or failure it returns immediately — v1 is never
   consulted in this branch.
2. **v1 fallback**: only reached if `/dmut2` was not a directory. If the file
   `/dmut/index.dmut` exists (via `fileExists`, `dmut.go:13-22`, which also rejects
   directories), it runs `mutations.ParseAndRunMutations(pguri, "/dmut/index.dmut")`
   (v1 API, `dmut.go:44`).
3. If neither path exists, no mutations run at all; `srv.LogInfo("no dmut found, skipping mutations")` and status is set to `"skipped"` (`dmut.go:52-53`).

So the two dependencies are not dead weight from an import-graph point of view — both are
live code paths — but only one runs per process, decided purely by which hardcoded
filesystem path is present. See §9 for why this is worth reconsidering.

## 3. Trigger conditions

`tryRunDmut(srv, pguri)` is called from exactly two places in `main.go`:

1. **Startup**, unconditionally (no feature flag/env var gates the call itself — the
   gating is purely the on-disk file/dir check inside `tryRunDmut` described in §2):
   ```go
   if err = tryRunDmut(&srv, db_url); err != nil {
       srv.LogError("when running mutations: ", err)
   }
   ```
   `main.go:178-180`. This runs after the Postgres connection pool is established and
   connectivity is confirmed (`main.go:93-140`), but *before* `SetupPG`, `setupPg3Routes`,
   `SetupAuth`, OAuth/SAML setup, and `srv.PgReloadTables()` (`main.go:182-202`). So the
   schema/roles/functions dmut creates are expected to exist before the server introspects
   Postgres to build its routing table and auth capabilities.
2. **`SIGUSR1` signal handler**, set up as a goroutine at `main.go:297-316`:
   ```go
   case syscall.SIGUSR1:
       srv.LogWarn("Received, SIGUSR1, relaunching dmut")
       if err = tryRunDmut(&srv, db_url); err != nil {
           srv.LogError("when running mutations: ", err)
       } else {
           srv.PgReloadTables()
           ReloadLoginFunctions(&srv)
       }
   ```
   (`main.go:305-312`). This is the **hot-reload path**: an operator (or deploy script)
   sends `SIGUSR1` to the running process to re-run migrations without a restart. The
   full sequence, in order, only proceeds past each step on success of the previous one:
   1. `tryRunDmut` — re-parses and re-applies the mutation file(s) exactly as at startup
      (same `/dmut2` vs `/dmut/index.dmut` detection logic).
   2. If (and only if) that succeeded: `srv.PgReloadTables()` — re-introspects Postgres
      (`information_schema`/catalogs) to rebuild the server's in-memory table/column/FK
      metadata used to answer REST-style requests, since migrations may have added,
      removed, or altered tables/columns.
   3. `ReloadLoginFunctions(&srv)` (`auth.go:109-123`) — re-queries
      `information_schema.routines`/`.parameters` to detect which of the three
      auth-related Postgres functions (login, external login, session-check) currently
      exist, and updates `srv.AvailableLoginFunctions` accordingly (see §8).

   Note the asymmetry with the startup path: at startup, `PgReloadTables()` and
   (implicitly, inside `SetupAuth`) function detection happen regardless of whether
   `tryRunDmut` returned an error (the startup `if` only logs the error and falls
   through, `main.go:178-190`); on `SIGUSR1`, the reload/reload-functions steps are
   explicitly skipped (`else` branch) if `tryRunDmut` failed. So a mid-run migration
   failure via signal leaves the server's cached schema/function knowledge exactly as it
   was before the signal — it does not attempt to resync partial state.

There is no separate env var to disable dmut at either call site; the only way to skip it
is to not have `/dmut2` or `/dmut/index.dmut` present in the container filesystem.

## 4. `SwServer.DmutStatus`

Declared as a plain `string` field on `SwServer` (`sw/defs.go:26`). Set only from
`dmut.go`, to one of:

| Value | Meaning | Set at |
|---|---|---|
| `"ok"` | v1 path ran and `ParseAndRunMutations` returned no error | `dmut.go:49` |
| `"skipped"` | Neither `/dmut2` (dir) nor `/dmut/index.dmut` (file) exists | `dmut.go:53` |
| `err.Error()` (arbitrary error text) | Either the v2 or v1 run failed; the field is set to the *stringified Go error*, not a fixed enum value | `dmut.go:36` (v2 failure), `dmut.go:46` (v1 failure) |

Notably there is **no explicit `"ok"` (or any) assignment on the v2 success path** — if
`mut2.ReadAndRunMutations` succeeds, `tryRunDmut` returns `nil` at `dmut.go:38` without
ever touching `srv.DmutStatus`. Since `DmutStatus` is a zero-initialized `string` field on
a struct that is only ever constructed once per process (`main.go:82`), this means: on a
successful v2 run, `DmutStatus` stays whatever it was before (i.e. `""` on first successful
startup run, since no code sets it earlier) — it is **not** set to `"ok"` the way the v1
path is. This is an inconsistency (see §9).

`DmutStatus` is exposed verbatim as the `dmut` field of the JSON body returned by
`GET /heartbeat` (`main.go:322-341`, field `status.Dmut = srv.DmutStatus`, `main.go:337`).
This is the only consumer of the field in this codebase — it's a simple health/ops signal,
not used to gate any other logic (e.g. the server keeps serving requests even if
`DmutStatus` holds an error string; see §7).

## 5. Migration file format — `test/dmut/index.dmut` (v1 DSL)

This is the **v1** ("`github.com/ceymard/dmut`", not v2) file format — a custom SQL-based
DSL parsed by a hand-written lexer/grammar (`~/go/pkg/mod/github.com/ceymard/dmut@v0.3.1/parser/grammar.go`).
Structure, illustrated by the actual fixture:

```
mutation roles
  create role "~anonymous";
  create role "~authenticated";

mutation api depends on roles
  create schema api;
  grant usage on schema api to "~authenticated";
  grant usage on schema api to "~anonymous";

mutation basic_tables depends on api
  CREATE TABLE api.items ( ... );
  ...
  grant all on table api.users to "~anonymous";
```

Key structural points, cross-referenced against the parser/mutation packages:

- **Top level** is a sequence of `mutation <name> [depends on <name>[, <name>...]] <statements...>` blocks (`grammar.go`'s `MutationDecl`), plus optional `include "<path>";` directives that splice in other `.dmut` files relative to the including file (`fromast.go:36-45`).
- **Dependency names** may end in `.*` (e.g. `depends on foo.*`) to depend on every mutation whose name matches `foo(\..*)?` as a regex (`fromast.go:52-56`, `93-121`) — a simple wildcard/prefix-dependency mechanism.
- Mutation names form a DAG; cycles are detected and rejected (`mutation.go:205-241`, `CheckParentCycle`).
- **Statements** inside a mutation are parsed into typed forms so `dmut` can auto-generate the `DOWN` statement for common DDL, or fall back to raw pass-through for `UP` only:
  - `CREATE [OR REPLACE] TABLE|VIEW|MATERIALIZED VIEW|EXTENSION|SCHEMA|TYPE|ROLE <id> ...` → auto `DOWN`: `DROP <kind> <id>;` (`parser/impl.go:83-85`).
  - `CREATE [UNIQUE] INDEX <name> ON <table> ...` → auto `DOWN`: `DROP INDEX [<schema>.]<name>` (`parser/impl.go:62-69`).
  - `CREATE POLICY|TRIGGER <name> ... ON <target> ...` → auto `DOWN`: `DROP POLICY|TRIGGER <name> ON <target>;` (`parser/impl.go:71-73`).
  - `CREATE FUNCTION <name>(<args>) ...` → auto `DOWN`: `DROP FUNCTION <name> (<args>)` (`parser/impl.go:75-81`).
  - `GRANT <perms> ON [<kind>] <id> TO <role>;` → auto `DOWN`: `REVOKE <perms> ON <kind> <id> FROM <role>;` (`parser/impl.go:41-47`).
  - `ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;` → auto `DOWN`: `ALTER TABLE <t> DISABLE ROW LEVEL SECURITY;` (`parser/impl.go:37-39`).
  - Explicit `up ... ;` / `down ... ;` blocks let you write arbitrary custom SQL for either direction, using `$...$`-delimited multiline strings so semicolons inside the SQL don't terminate the statement early (`grammar.go`'s `UpOrDownStmt`, `MultilineString`).
  - `COMMENT ON <target> IS '...';` parses into a fifth statement kind, `CommentStatement` (`grammar.go:46-49`). Its `Up()` works fine (raw pass-through of the matched tokens, `parser/impl.go:9-22`), but there is **no `Down()` case for it**: `ASTStatement.Down()` only handles `CreateStatement`/`GrantStatement`/`UpOrDownStmt`/`RlsStatement` and falls through to `panic("Not implemented")` for anything else (`parser/impl.go:24-35`) — which, given the grammar is a closed 5-way union, means specifically `CommentStatement`. So a bare `comment on table x is '...';` in a `.dmut` file applies (and hashes) fine, but the moment dmut needs to down that mutation (because it changed, was removed, or is being down-tested — see the testing pass below) the run **panics** instead of returning an error. Statements that don't fit any of the five recognized forms at all are simply **parse errors** from `ParseString`/`GetMutationsInFile` (`fromast.go:31-34`), not silent passthrough.
- Files are first rendered through the **pongo2** (Django/Jinja-like) template engine before parsing, with `env` exposed as a global function so `.dmut` files can interpolate environment variables (`fromast.go:15-26`, `pongo2.Globals["env"] = os.Getenv`).
- Each mutation's **hash** is a SHA-256 over its parents' hashes plus a whitespace/comment-normalized (tokenized) concatenation of all its `Up`/`Down` SQL text (`mutation.go:171-202`, `mutation.go:31-46` `DigestBuffer.AddStatement`) — this is what drives the up/down diffing in §1.
- On Postgres, applied mutations are tracked in a bookkeeping table `dmut.mutations(hash, name, up, down, children, date_applied)`, itself created/dropped by a synthetic bootstrap mutation `dmut.base` that every run prepends (`mutation.go:302-326`, `pgrunner.go:36-54`, `85-122`). The `dmut` schema name is itself overridable via `DMUT_SCHEMA` env var (default `"dmut"`) — see §6.
- **Testing pass**: after applying, v1 additionally re-verifies each non-bootstrap mutation by down-then-up-ing it alone inside nested savepoints, to catch mutations that aren't independently reversible (`runner.go:236-257`). Any failure here rolls the *entire* run back (`runner.go:213-226`), even though the "real" apply already logically succeeded — see §7.

## 6. Environment variables

Read directly inside `dmut.go`/`main.go` for the integration glue:

- None — `dmut.go` reads **no environment variables itself**; `getenvOrDefault` is not
  called anywhere in this file. The only "configuration" at the call-site level is the
  hardcoded paths `/dmut2` and `/dmut/index.dmut` (`dmut.go:28`, `42`).
- The Postgres connection string is **reused** from the same `db_url` the rest of the
  server uses: `getenvOrDefault("DATABASE_URL", "PGRST_DB_URI", "")` (`main.go:81`),
  passed into `tryRunDmut(&srv, db_url)` as `pguri` at both call sites (`main.go:178`,
  `307`). There is no dmut-specific connection string.

Inside the vendored libraries themselves (not read by this server's own code, but
relevant to behavior if set in the container environment):

- **v1**: `DMUT_SCHEMA` — overrides the bookkeeping schema name, default `"dmut"`
  (`~/go/pkg/mod/github.com/ceymard/dmut@v0.3.1/mutations/mutation.go:303`). `DMUT_SHOW_ALL` — if non-empty, prints every up/down step even during the internal "testing" pass, not just real applies (`.../mutations/runner.go:147,163`).
- **v2**: no equivalent env vars were found read inside `mutations/run_mutations.go`;
  its `MutationRunnerOptions{Verbose, Commit, Override, All}` (`.../v2@v2.0.1/mutations/run_mutations.go:8-13`) are Go struct fields, not env-driven, and `dmut.go` only ever sets `Commit: true` (`dmut.go:33-34`), leaving `Verbose`, `Override`, and `All` at their zero values (`false`) for every run triggered by this server.

## 7. Error handling — does the server still start?

**Yes — a failed migration does not stop the server from serving requests.** Both call
sites treat `tryRunDmut`'s error as non-fatal:

- Startup: `if err = tryRunDmut(...); err != nil { srv.LogError(...) }` — logs and falls
  straight through to the rest of `main()` (`main.go:178-180`); there is no `return`,
  `os.Exit`, or `log.Fatal`. The server goes on to call `SetupPG`, `SetupAuth`,
  `PgReloadTables`, and ultimately `http.ListenAndServe` regardless.
- `SIGUSR1`: same pattern — the error is logged and the goroutine just loops back to wait
  for the next signal (`main.go:307-309`); the process is never killed.

Practically, this means a broken migration typically leaves the database in whatever
state the transactional rollback left it (both v1's `RunMutations`, `.../dmut@v0.3.1/mutations/runner.go:202-260`, and v2's `RunAllMutations`, `.../v2@v2.0.1/mutations/run_mutations.go:106-164`, wrap the whole attempt in a savepoint/transaction and roll back on any error) — i.e. the *database* is left consistent (old schema intact, nothing half-applied) — but the *server process* proceeds to boot and serve HTTP traffic against that old/unchanged schema, only surfacing the problem via `srv.DmutStatus` on `/heartbeat` (§4) and the error log line. There is no retry loop and no readiness gate tied to dmut success.

## 8. Relationship with `ReloadLoginFunctions`

`ReloadLoginFunctions(srv *sw.SwServer)` (`auth.go:109-123`) queries
`information_schema.routines`/`.parameters` (via a view-like SQL constant
`SQL_CHECK_FUNCTIONS` defined just above it, `auth.go:88-107`) to determine, as three
booleans (`srv.AvailableLoginFunctions.{Login,ExternalLogin,SessionCheck}`,
`sw/defs.go:27-31`), whether specific stored functions currently exist in the `auth`
schema. These are the Postgres-side functions that `auth.go`'s HTTP handlers (`/auth/login`,
external/SSO login, and per-request session validation — documented in the separate auth
doc) call via `SELECT auth.<fn>(...)` to actually check credentials/sessions; this file
only detects their *presence*, it doesn't implement them.

The reason this needs to be re-run after a `dmut` migration is that dmut owns creation of
those SQL functions (a `CREATE FUNCTION auth.login(...)` etc. would typically live in a
`.dmut` mutation, using the "auto-`DROP`-on-change" behavior from §5, or a v2 `meta`
block). If an operator ships a migration that adds, removes, or changes the signature of
one of these functions and triggers a hot-reload via `SIGUSR1`, the server's cached
`AvailableLoginFunctions` booleans (populated once at `SetupAuth` time during startup)
would otherwise go stale — e.g. still reporting session-checking disabled after a
migration just added the session-check function, or vice-versa. Hence the reload sequence
in §3 explicitly re-queries them immediately after a successful `tryRunDmut` +
`PgReloadTables`, so login behavior gated by `AvailableLoginFunctions.SessionCheck`
(logged at `auth.go:118-122`) reflects the just-applied schema without a full process
restart.

## 9. Notes / things to reconsider for the rewrite

- **Two live major versions, chosen by directory presence, not config.** `dmut.go`
  imports and calls both v1 (`github.com/ceymard/dmut`) and v2
  (`github.com/ceymard/dmut/v2`), selecting between them purely by checking whether
  `/dmut2` is a directory (`dmut.go:29-30`). This is fragile/implicit: there's no env var
  or explicit "engine" setting, just filesystem archaeology, and the two engines use
  **completely different migration file formats** (v1: single `.dmut` file with a custom
  SQL-superset DSL, §5; v2: a directory of YAML files with `sql`/`meta`/`needs`/
  `__revision`/`__namespace` — see the v2 README bundled with the module, materially
  richer feature set: revisions, namespaces, heavy-vs-lightweight statement split, an
  actual test-database dry run). A new server should pick exactly one engine/format and
  make the choice explicit and configurable (env var or config file), not path-sniffing.
- **Only v1 has a fixture/example in this repo.** `test/dmut/index.dmut` is v1-format;
  there is no `/dmut2`-style YAML example anywhere in `_legacy`, even though v2 is
  imported and would take priority if `/dmut2` existed. This strongly suggests the v1→v2
  migration was started (dependency added, code path wired) but never completed/exercised
  in this codebase — dead-in-practice code that still compiles and runs.
- **Hardcoded absolute paths.** `/dmut2` and `/dmut/index.dmut` (`dmut.go:28,42`) assume a
  specific container filesystem layout (a `dmut`/`dmut2` directory mounted or baked in at
  the filesystem root) and are not overridable via env var, unlike almost every other
  path/behavior in this server (which mostly goes through `getenvOrDefault`). The
  README's French description ("`/server/dmut/index.dmut`", `README.md:57`) doesn't even
  match the actual hardcoded path in code (`/dmut/index.dmut`, no `/server` prefix) — a
  stale doc vs. code mismatch worth fixing either way.
- **Inconsistent `DmutStatus` semantics.** v1 success sets `"ok"`; v1 skip sets
  `"skipped"`; both v1 and v2 failure set the raw Go error string; v2 **success sets
  nothing**, leaving whatever value was there before (typically `""` on a fresh process) —
  see §4. A rewrite's equivalent status field should have a small closed enum
  (`ok | skipped | error | not_yet_run`) with a message field held separately, so
  `/heartbeat` consumers don't have to guess whether an empty string means "not run yet"
  or "ran fine, nothing to say."
- **No fatal/gating behavior on migration failure.** Both call sites are fire-and-forget:
  a failed migration is logged but the HTTP server starts anyway and serves traffic
  against a schema that may be missing tables/roles/functions the rest of the code
  assumes exist (§7). This is a reasonable choice for zero-downtime hot-reload via
  `SIGUSR1` (you don't want a bad migration to kill an already-running server), but is
  questionable for the *startup* path, where serving requests against a never-successfully-migrated
  database is likely to produce confusing runtime errors elsewhere rather than a clear
  boot failure. Worth an explicit policy: e.g. fail-fast on first-ever startup, best-effort
  on hot-reload.
- **v1's `COMMENT ON` statements panic on down.** As detailed in §5, `CommentStatement`
  is a fully-parseable, fully-hashable statement kind with no corresponding `Down()`
  implementation, so it `panic("Not implemented")`s (`parser/impl.go:24-35`) the moment
  dmut needs to down a mutation containing one — which happens routinely, since v1 runs
  an internal down-then-up "testing" pass on every non-bootstrap mutation on *every* run
  (`runner.go:236-257`), not just when content actually changes. This is a panic
  triggered by ordinary migration-file content, not a returned `error`. `main.go`'s
  signal-handler goroutine (`main.go:297-316`) that calls `tryRunDmut` on `SIGUSR1` has no
  visible `recover()` of its own; `RecovererColored` (`recoverer.go:14-37`) is an
  `http.Handler` middleware wrapping the chi router only, so it would not catch a panic
  originating in that goroutine — a `.dmut` file with a `comment on ...` statement could
  crash the whole process on `SIGUSR1`, not just fail the migration.
- **v1's Postgres-vs-SQLite detection can silently pick the wrong backend.** v1's
  `ParseAndRunMutations` decides Postgres vs. SQLite purely via
  `regexp.MustCompilePOSIX("^(postgres|pg)://")` matched against the connection string
  (`runner.go:296,320-330`; the accompanying `isPgUrl` helper at `runner.go:298` is dead
  code that unconditionally returns `true`). That regex requires the string to start with
  the literal substrings `postgres://` or `pg://` — it does **not** match the equally
  valid `postgresql://` scheme (`"postgresql://"` has `ql` where the regex expects `://`
  right after `postgres`). Since this server's own Postgres connection (`pgxpool.New`,
  `main.go:94`) happily accepts `postgresql://` URIs, a `DATABASE_URL`/`PGRST_DB_URI`
  using that scheme would make the *server* connect to Postgres fine while v1 dmut
  quietly routes to `NewSqliteRunner` instead — attempting to open the Postgres URI as a
  SQLite file path. Also note `tryRunDmut(&srv, db_url)` at `main.go:178` is called
  unconditionally, even when `db_url == ""` (no database configured at all — the
  `if srv.HasPg()` guard at `main.go:182` covers `SetupPG`/`SetupAuth` but not the dmut
  call), so an empty string goes through the same regex-then-SQLite-runner path.
- **v1 leaks a Postgres connection and can leave an idle-in-transaction session per run.**
  `NewPgRunner` (`pgrunner.go:28-34`) opens a bare `pgx.Connect`; the `Runner` interface
  (`runner.go:18-27`) has no `Close` method, and neither `ParseAndRunMutations`
  (`runner.go:304-340`) nor its caller in `dmut.go` ever closes it — every `tryRunDmut`
  call (startup, plus once per `SIGUSR1`) opens a connection that is never released.
  Separately, `RunMutations`'s deferred commit/rollback logic (`runner.go:213-226`) only
  issues `COMMIT` when `should_test` is true; on the common "nothing changed" path it logs
  `"dmut: no changes, not doing anything"` and does **neither** `COMMIT` nor `ROLLBACK`,
  leaving the `BEGIN` from `SavePoint("")` (`runner.go:209`, `pgrunner.go:61-69`) open on
  that leaked connection. Repeated `SIGUSR1` signals with no schema changes would
  accumulate idle-in-transaction connections. v2's equivalent entry point,
  `ReadAndRunMutations`, does `defer runner.Close()` (`.../v2@v2.0.1/mutations/run_mutations.go:177`)
  — this is a concrete, self-contained argument for not reusing v1's runner in a rewrite.
- **`go.mod` keeps both dependencies indefinitely.** `github.com/ceymard/dmut v0.3.1` and
  `github.com/ceymard/dmut/v2 v2.0.1` are both direct, non-indirect requires
  (`go.mod:8-9`). Even ignoring the runtime path-selection issue above, this roughly
  doubles the migration-related dependency surface (two SQL parsers, two Postgres
  runners, two file-format readers) for a single logical feature.
