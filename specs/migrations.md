
# DMUT

rel provides `dmut` for handling database migrations/mutations — `github.com/ceymard/dmut/v2`
(`mutations` package), a dependency-graph migration tool (not sequential up/down) : a mutation
declares which others it depends on, and changing one recursively downs and re-ups it and every
mutation depending on it. See that module's own `README.md` for the full mutation-file format
(`sql`/`meta`, `needs`/`meta_needs`, `children`, `__namespace`, `__revision`/`new_sql`/
`new_needs`) — this document only covers how rel drives it, not the file format itself.

## Configuration

* `dmut.path` (default `/dmut`) : the directory containing the mutation files. Named `.path`,
  not `.directory`, to match every other filesystem-location key's own naming (`http.static.path`,
  `pg.query.wellknown_path`).
* `dmut.reload_drain_timeout` (default `30`, seconds) : how long a `SIGUSR1` reload waits for
  requests already in flight to finish before proceeding regardless — see ## Reloading.

## Execution

dmut runs once, always, at every rel startup — never optional to invoke, though its effect is a
no-op when there's nothing to change (dmut's own delta computation: nothing local differs from
what's recorded in `__dmut__.mutations`, so nothing is downed or upped, and dmut's own test phase
is skipped too — see its README's "How a run works"). Effectively optional in practice, not in
mechanism : a deployment that doesn't want dmut simply doesn't create `dmut.path` (see below),
never a config flag that skips calling it.

**Startup order : dmut runs BEFORE introspection, always.** rel's introspected schema cache
(`pg.DbInfos`) must reflect whatever dmut leaves the database in, not whatever it looked like
before — running introspection first would build `pg.DbInfos` from a schema dmut is about to
change out from under it. Concretely, `cmd/rel/main.go`'s boot sequence becomes : resolve
connection URIs → run dmut against the primary/admin URI → `pg.NewInfosAdminQuery` (introspection
+ build the request-serving pool) → build the `/rpc` registry → start serving. This is a real
reordering of the existing boot sequence, not an addition alongside it.

* **`dmut.path` doesn't exist on disk** : dmut is skipped entirely, silently (an `info`-level log
  line, not a warning) — no directory means the feature isn't in use, not a misconfiguration.
  Matches the "optional in practice" framing above : a deployment that never creates `/dmut` (or
  whatever it's configured to) just doesn't pay for a dmut run, no error, no explicit opt-out flag
  needed.
* **dmut.path exists but the run fails** (a mutation's SQL errors, a down doesn't round-trip in
  the test phase, a dependency cycle, ...) : rel logs the error and CONTINUES — startup is never
  aborted by a dmut failure, at boot or on reload (## Reloading). dmut's own transaction discipline
  means a failed run is fully rolled back (see its README : "Everything happens inside a single
  transaction, per namespace... Failure at any step halts the process and nothing is applied"), so
  the database is left exactly as it was before the attempt — rel proceeds to introspect that
  unchanged schema, same as if dmut had found nothing to do. This is a deliberate choice, not an
  oversight : a broken migration file shouldn't be able to take a running deployment offline by
  itself, and the unchanged-database guarantee is what makes "log and continue" safe rather than
  reckless — there's no half-migrated state for rel to introspect into.

## dmut's own logging

`mutations.ReadAndRunMutations`'s `Output io.Writer` option (added to dmut itself for this
integration) redirects dmut's own progress/notice lines into rel's structured logger instead of
raw stdout — one `logger.Info(line, "component", "dmut")` call per dmut log line, at `info` level
(migrations only run at startup and on an operator-triggered reload — infrequent, deliberate
events worth surfacing by default, not hidden behind `logging.level=debug`).

## Reloading

At any moment, rel can be sent `SIGUSR1` to have it reload the dmut mutations — repeatably, unlike
the interrupt/terminate signals that trigger a one-shot graceful shutdown ; rel keeps a persistent
signal handler for `SIGUSR1` for as long as the process runs, not a single-use one.

On receiving it :

`http.Server.Handler` is, permanently (not just during a reload), a small reload-aware wrapper
holding an `atomic.Pointer` to the current "real" mux (`server.NewRelHandler`/`rpc.NewHandler`
composed together, same as today) — this is what makes both "serve a 503 page during reload" and
"swap to the new schema" safe : the `http.Server.Handler` field itself is written exactly once, at
startup, and never touched again ; only the pointer inside the wrapper is ever swapped, which is
what an `atomic.Pointer` is for. Concurrently mutating `http.Server.Handler` itself while the
server is accepting connections would be a data race independent of whether requests happen to be
in flight at that moment — the wrapper exists specifically so nothing ever needs to do that.

1. **New requests stop being served immediately.** The wrapper flips into maintenance mode :
   every subsequent `/rel` and `/rpc` request, for as long as the reload is in progress, gets
   `503 Service Unavailable` and a small, fixed convenience page (plain text/minimal HTML, no
   templating) saying migrations are running — never queued behind the reload, never served
   against a schema that might be mid-change.
2. **rel waits for requests already in flight to finish**, bounded by `dmut.reload_drain_timeout`
   (default 30s). Past that, the wrapper CANCELS every still-running request's context (the
   drain tracking wraps each request's context in its own `context.WithCancel`, fired on timeout)
   rather than simply proceeding around them — pgx honors context cancellation and releases
   whatever Postgres locks that request was holding, which matters here specifically because
   dmut's own DDL (no default lock timeout) would otherwise block indefinitely on a straggler's
   locks with the timeout having bought nothing. This is what makes "never block a reload forever"
   actually true, not just "proceeds and hopes" : the timeout bounds real wall-clock time to start
   dmut, not just how long rel is willing to wait before trying.
3. **dmut runs again**, against the same primary/admin connection introspection itself uses,
   with the same non-fatal failure handling as startup (## Execution above) : on failure, rel logs
   the error, skips reintrospection/registry-rebuild entirely (the database is unchanged, per
   dmut's own rollback guarantee — nothing to pick up), flips the wrapper back out of maintenance
   mode, and resumes serving under the OLD schema/registry — exactly as if the reload had never
   been requested.
4. **On success, rel reintrospects** — a fresh `pg.DbInfos`' worth of `Types`/`Functions`/
   `Relations`, built via the SAME introspection-connection dance `pg.NewInfosAdminQuery` already
   does (INCLUDING the anonymous-role existence check, `specs/authentication.md ## Anonymous
   role existence` — that document already promises this check runs "at startup, and any future
   schema reload" ; a mutation file is a completely ordinary way to `CREATE ROLE` the configured
   anonymous role for the first time, and reload must pick that up without a full process restart),
   but reusing the EXISTING request-serving pool rather than closing and reopening it (no reason to
   churn every pooled connection just because the schema changed — in-flight requests are already
   drained/cancelled by this point, so there's no concurrent-access hazard in reusing the pool
   either way, only unnecessary connection cost to avoid).
5. **The `/rpc` registry is rebuilt** from the new `pg.DbInfos` (`rpc.BuildRegistry`), same as at
   startup — new/changed route functions, anonymous-authorization caching (`specs/
   rpc.md ## Anonymous route authorization`), and the `PUBLIC`-executable warning
   all re-run against the post-reload schema.
6. **A fresh inner mux is built** via `boot.BuildMux` — the same function the initial startup mux
   is built with, so the two call sites cannot drift on what the mux actually contains — composing
   `/rel` (`server.NewRelHandler`), `/rpc/` (`rpc.NewHandler`), and, when at least one
   `http.static.path` directory exists, `/static/`, against the new `pg.DbInfos`/registry, all
   wrapped uniformly in `websec.Middleware` (CORS/CSP). The result is stored into the wrapper's
   `atomic.Pointer` — a single pointer store, not a write to `http.Server.Handler` itself.
7. **The wrapper flips out of maintenance mode** ; new requests resume being served, against the
   new inner mux.

Well-known queries are NOT reloaded by this mechanism yet — well-known queries themselves aren't
implemented at all as of this document (`server/rel.go` rejects `WellKnown` outright, see
`specs/TODO.md`) ; once they exist, reloading them on the same `SIGUSR1` is the obvious pairing,
but there's nothing to reload today. Noted here as a forward reference, not scope for this pass.

## Not covered by this document

* The mutation file format itself (`sql`/`meta`/`needs`/`__namespace`/`__revision`/...) — see
  `github.com/ceymard/dmut/v2`'s own `README.md`, kept as the single source of truth so this
  document doesn't drift from it.
* `dmut`'s CLI (`dmut apply`/`dmut test`/`dmut down`/...) — rel drives the `mutations` package
  directly as a library, never shells out to the `dmut` binary.
