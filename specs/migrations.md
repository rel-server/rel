# DMUT

rel provides `dmut` for handling database migrations/mutations — `github.com/ceymard/dmut/v2`
(`mutations` package), a dependency-graph migration tool (not sequential up/down) : a mutation
declares which others it depends on, and changing one recursively downs and re-ups it and every
mutation depending on it. See that module's own `README.md` for the full mutation-file format
(`sql`/`meta`, `needs`/`meta_needs`, `children`, `__namespace`, `__revision`/`new_sql`/
`new_needs`) — this document only covers how rel drives it, not the file format itself.

## Configuration

* `dmut.path` (default `/dmut`) : the directory containing the mutation files. Named `.path`, matching every other filesystem-location key's own naming (`http.static.path`, `pg.query.wellknown_path`).
* `dmut.reload_drain_timeout` (default `30`, seconds) : how long a `SIGUSR1` reload waits for requests already in flight to finish before proceeding regardless — see ## Reloading.

## Execution

dmut runs once, always, at every rel startup — never optional to invoke. Its effect is a no-op when nothing local differs from `__dmut__.mutations` (dmut's own delta computation) : nothing is downed or upped, and dmut's own test phase is skipped too (its README's "How a run works"). A deployment opts out only by not creating `dmut.path` (below) ; there is no config flag that skips calling it.

**Startup order : dmut runs BEFORE introspection, always.** `cmd/rel/main.go`'s boot sequence is : resolve connection URIs → run dmut against the primary/admin URI → `pg.NewInfosAdminQuery` (introspection + build the request-serving pool) → build the `/route` registry → start serving.

> Why: rel's introspected schema cache (`pg.DbInfos`) must reflect whatever dmut leaves the database in, not whatever it looked like before — introspecting first would build `pg.DbInfos` from a schema dmut is about to change out from under it.

* **`dmut.path` doesn't exist on disk** : dmut is skipped entirely, silently (an `info`-level log line, not a warning) — no directory means the feature isn't in use, not a misconfiguration.
* **`dmut.path` exists but the run fails** (a mutation's SQL errors, a down doesn't round-trip in the test phase, a dependency cycle, ...) : rel logs the error and CONTINUES — startup is never aborted by a dmut failure, at boot or on reload (## Reloading). dmut's own transaction discipline ("Everything happens inside a single transaction, per namespace... Failure at any step halts the process and nothing is applied") means the database is left exactly as it was before the attempt ; rel proceeds to introspect that unchanged schema, same as if dmut had found nothing to do.
  > Why: a broken migration file must not be able to take a running deployment offline by itself ; the unchanged-database guarantee is what makes "log and continue" safe rather than reckless.

## dmut's own logging

`mutations.ReadAndRunMutations`'s `Output io.Writer` option (added to dmut itself for this integration) redirects dmut's own progress/notice lines into rel's structured logger instead of raw stdout — one `logger.Info(line, "component", "dmut")` call per dmut log line, at `info` level.

> Why: migrations only run at startup and on an operator-triggered reload — infrequent, deliberate events worth surfacing by default, not hidden behind `logging.level=debug`.

## Reloading

At any moment, rel can be sent `SIGUSR1` to have it reload the dmut mutations — repeatably, unlike the interrupt/terminate signals that trigger a one-shot graceful shutdown. rel keeps a persistent signal handler for `SIGUSR1` for the life of the process.

`http.Server.Handler` is, permanently, a small reload-aware wrapper holding an `atomic.Pointer` to the current "real" mux (`server.NewRelHandler`/`route.NewHandler` composed together). The `http.Server.Handler` field itself is written exactly once, at startup, and never touched again ; only the pointer inside the wrapper is ever swapped.

> Why: concurrently mutating `http.Server.Handler` itself while the server is accepting connections would be a data race independent of whether requests happen to be in flight — the wrapper exists so nothing ever needs to do that.

On receiving `SIGUSR1` :

1. **New requests stop being served immediately.** The wrapper flips into maintenance mode : every subsequent `/rel` and `/route` request, for as long as the reload is in progress, gets `503 Service Unavailable` and a small, fixed convenience page (plain text/minimal HTML, no templating) saying migrations are running.
2. **rel waits for requests already in flight to finish**, bounded by `dmut.reload_drain_timeout` (default 30s). Past that, the wrapper CANCELS every still-running request's context (the drain tracking wraps each request's context in its own `context.WithCancel`, fired on timeout).
   > Why: pgx honors context cancellation and releases whatever Postgres locks that request was holding — dmut's own DDL (no default lock timeout) would otherwise block indefinitely on a straggler's locks.
3. **dmut runs again**, against the same primary/admin connection introspection itself uses, with the same non-fatal failure handling as startup (## Execution) : on failure, rel logs the error, skips reintrospection/registry-rebuild entirely, flips the wrapper back out of maintenance mode, and resumes serving under the OLD schema/registry — exactly as if the reload had never been requested.
4. **On success, rel reintrospects** — a fresh `pg.DbInfos`' worth of `Types`/`Functions`/`Relations`, built via the same introspection-connection dance `pg.NewInfosAdminQuery` already does, including the anonymous-role existence check (`specs/authentication.md ## Anonymous role existence` — a mutation file may `CREATE ROLE` the configured anonymous role for the first time, and reload must pick that up without a full process restart) — but reusing the EXISTING request-serving pool rather than closing and reopening it. In-flight requests are already drained/cancelled by this point, so reusing the pool has no concurrent-access hazard.
5. **The `/route` registry is rebuilt** from the new `pg.DbInfos` (`route.BuildRegistry`), same as at startup — new/changed route functions, anonymous-authorization caching (`specs/route.md ## Anonymous route authorization`), and the `PUBLIC`-executable warning all re-run against the post-reload schema.
6. **A fresh inner mux is built** via `boot.BuildMux` — the same function the initial startup mux is built with — composing `/rel` (`server.NewRelHandler`), `/route/` (`route.NewHandler`), and, when at least one `http.static.path` directory exists, `/static/`, against the new `pg.DbInfos`/registry, all wrapped uniformly in `websec.Middleware` (CORS/CSP). The result is stored into the wrapper's `atomic.Pointer` — a single pointer store, not a write to `http.Server.Handler` itself.
7. **The wrapper flips out of maintenance mode** ; new requests resume being served, against the new inner mux.

Well-known queries are not reloaded by this mechanism — well-known queries aren't implemented at all as of this document (`server/rel.go` rejects `WellKnown` outright, see `specs/TODO.md`). Reloading them on the same `SIGUSR1` is the intended pairing once they exist.

## Not covered by this document

* The mutation file format itself (`sql`/`meta`/`needs`/`__namespace`/`__revision`/...) — see `github.com/ceymard/dmut/v2`'s own `README.md`, the single source of truth.
* `dmut`'s CLI (`dmut apply`/`dmut test`/`dmut down`/...) — rel drives the `mutations` package directly as a library, never shells out to the `dmut` binary.
