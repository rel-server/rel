# Reload

## Current issue

- Dmut is completely embedded into rel. While this is my mutation tool of choice, this might not be other people's.
- We still need a way to trigger changes in the database and have rel reloaded

## Solution

Decouple completely rel from dmut. The `dmut` package, its go.mod dependency, and the `dmut.path`/`dmut.reload_drain_timeout` config keys are removed entirely ; users who still want dmut invoke it themselves via `reload.cmd`.

- Configuration entry `reload.cmd` (default empty)
- Configuration entry `reload.timeout` (default 120 seconds) : command is aborted after this timeout and is considered failed
- Configuration entry `reload.drain_timeout` (default 30 seconds, carried over from the previous `dmut.reload_drain_timeout` default) : time to wait for in-flight requests to finish before launching the reload

The command is run everytime dmut was to be run before this change, which is right before instrospection - at start or at reload. Its output is logged line by line into rel's own logs, tagged with `component=reload.cmd` and `stream=stdout`/`stream=stderr`. Once it has finished, the rest of the reload is executed (introspection / wellknown / templates / ...).

A line that parses as a JSON object has its keys merged into the log record on top of `component`/`stream`, for commands that support structured output.

When the command fails, the introspection reload is not performed, but wellknown and templates still get updated, rebuilt against the previous, still-loaded schema.

The configuration does not allow arrays, and this is problematic to specify a command line. `reload.cmd` is a single string. Interpolation is resolved first, on the raw string ; the result is then split into arguments using shell-style word-splitting and quoting rules only (e.g. `github.com/google/shlex`), with no variable expansion of its own.

Interpolation placeholders use `{}`, not `$`.

> Why: `$`/`${}` implies full shell semantics (globbing, command substitution, arithmetic) that aren't actually supported here ; a distinct syntax sets the right expectation.

- `{name}` resolves `name` as a configuration key if it contains a `.` (e.g. `{pg.uri}`), otherwise as an environment variable (e.g. `{HOME}`).
- `{name:default}` falls back to the literal `default` when `name` is unresolved.
- `{name}` with no default and an unresolved `name` is an error, aborting the reload before `reload.cmd` runs.

A substituted value is inserted as raw text, not as a pre-quoted token ; wrap it in quotes within `reload.cmd` if it may contain spaces, e.g. `reload.cmd: dmut --uri "{pg.uri}"`.

`{}` interpolation is only available within `reload.cmd` ; no other configuration value supports it.

A literal `{`/`}` anywhere in `reload.cmd` is always parsed as a placeholder attempt — there is no escape syntax. A command that itself needs a literal brace (an inline JSON fragment, say) must produce it at runtime (an env var, a here-string) rather than write it directly into `reload.cmd`.

The spawned process receives the full parent `os.Environ()`, unmodified, in addition to whatever was substituted into the command line — this allows for Docker-style configuration mechanisms that rely on the ambient environment.

It is the user's responsibility not to expose secrets through this mechanism ; all configuration is available. Reload is the only configuration that can read other configuration (as of now,) as interpolation is only performed when it is going to run.

## Impacts

Postgres's catalog visibility already limits what a role sees to what it holds some privilege on ; nothing a narrower `pg.query.user` role can't see is something a request served under that same role could ever have reached anyway. Narrowing introspection to it discovers nothing less than what's actually routable.

> Why: dmut's own requirement was DDL privileges to run migrations — a role limited to reading/referencing objects never needed that. Introspection is read-only, so it never needed more than the query-serving role already has.

`pg.query.user`/`pg.query.password` (the `PgQuery` struct's embedded `Login`), and the loader logic defaulting them from `pg.user`/`pg.password`, are removed entirely — `PgQuery`'s other fields (`AnonymousRole`, `MaxDepth`, `WellKnownDirs`) are unrelated to this and stay. There is no longer a distinction between a privileged connection and a query-serving one : `pg.user`/`pg.password` (or `pg.uri`) is used for introspection and for serving requests alike.

`docs/content/configuration/best-practices.md`, `docs/content/http/index.md`, and `docs/content/http/authentication.md` are updated to drop every mention of `pg.query.user`/`pg.query.password` and the privileged/narrower-role distinction they described.

Purging `dmut` costs more than the package itself :

- `dmut/run_test.go`, `dmut/integration_test.go`, and their fixture (`dmut/testdata/mutations.yml`) are deleted outright — they test the wrapper being removed, nothing to port.
- `boot/reload_test.go`'s `TestReloader_Reload_EndToEnd` drives its fixture (`boot/testdata/reload_fixture/mutation.yml`) through dmut's own YAML syntax and a documented dmut auto-down-generator quirk. It needs rewriting to exercise `reload.cmd` instead of the in-process call.
- `query_bench/main_test.go` builds its benchmark schema via in-process `dmut.Run` — the library gone, this either shells out to a real `dmut` binary (a new external tool dependency for CI/dev, undeclared anywhere today) or the hotel fixture is dropped, losing the realistic-depth rationale `specs/testing.md` gives for it.
- `docs/content/configuration/docker-deployment.md`'s `/dmut` volume section, and the unconditional `/dmut` `VOLUME`/`COPY` in `Dockerfile`/`Dockerfile.goreleaser`, need reframing around `reload.cmd` generically.
- Existing deployments relying on the zero-config `dmut.path` default (drop migrations at `/dmut`, no configuration needed) must start setting `reload.cmd` explicitly — a breaking behavior change for current users, not just an internal refactor.

`test/dmut`'s 14 migration files are converted, once, into a flat `schema.sql` applied via `postgres.WithInitScripts`, the same way `pg/testdata/schema.sql` already stands up the `director`/`movie` fixture — preserving the composite type, the range/exclusion constraint, the generated `tsvector` columns, and the self-referencing `staff` table exactly as they are today. `test/seed` is unaffected ; it has no dependency on how the schema was produced. `boot/reload_test.go`'s `TestReloader_Reload_EndToEnd` fixture is rewritten the same way, driving `reload.cmd` with a plain SQL script instead of a dmut mutation file.

> Why: both fixtures only ever needed dmut to produce a schema, not to exercise dmut's own migration mechanics — which is no longer rel's concern once dmut is external. A flat script loses nothing but the (now irrelevant) coverage of dmut's incremental up/down application.

`specs/testing.md`'s "`test/dmut` + `test/seed`" tier description, and its justification for existing, are updated to match — it no longer exercises `dmut` as an integration point, and its own line claiming `query/write_bench_test.go` depends on it is also wrong today (that file uses the flat `director`/`movie` fixture) and should be corrected in the same pass.

Reload should have its own documentation page explaining how it is meant to be used with a bring-your-own database migration tool. A paragraph should mention how to use dmut and link to it as it is particularly suited for "reload/change-often" scenarios

`{pg.host}`/`{pg.port}` interpolation requires the prerequisite `specs/pg-uri-precedence.md`.

> Why: `pg.host`/`pg.port` (not `db.host`/`db.port` — no `db.` prefix exists in this config) are otherwise unpopulated whenever the connection is configured via `pg.uri` alone, leaving nothing for `{pg.host}`/`{pg.port}` to resolve.

That means `reload.cmd` could be the target of some secrets itself — the literal command template might need to embed a token or password, not just derived config. That's left to `{ENV_VAR}` interpolation against the full process environment (already specified above), i.e. Docker/Swarm/Kubernetes secret injection, or the config file's own existing `$FILE$` indirection for the `reload.cmd` value as a whole ; rel does not add a separate secret-management mechanism for it.
