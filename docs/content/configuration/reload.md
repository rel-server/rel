# Reload

rel doesn't embed a migration tool. `reload.cmd` names whatever command you want run before
every reload — a migration tool, a plain SQL script via `psql`, or nothing at all if you manage
the schema some other way.

## When it runs

`reload.cmd` runs at every startup, before rel first introspects the schema, and again on every
`SIGUSR1`:

```sh
kill -USR1 <rel pid>
```

New requests get `503` for as long as a `SIGUSR1` reload is in flight; in-flight requests get up
to `reload.drain_timeout` (default 30s) to finish before rel cancels their context outright, so
a straggler can't hold a Postgres lock indefinitely against `reload.cmd`'s own DDL.

`reload.cmd` empty (the default) skips this step entirely — rel still reintrospects and rebuilds
its registries, it just runs nothing beforehand.

## Command line and interpolation

`reload.cmd` is a single string, split into arguments with shell-style word-splitting/quoting
rules (so quote an argument that contains spaces). Before splitting, `{name}`/`{name:default}`
placeholders are substituted :

- `{name}`, when `name` contains a `.`, resolves it as a configuration key — e.g. `{pg.uri}`.
  Otherwise it resolves as an environment variable — e.g. `{HOME}`.
- `{name:default}` falls back to the literal `default` when `name` is unresolved.
- `{name}` with no default and an unresolved `name` is an error — `reload.cmd` never runs.

A substituted value is inserted as raw text, not as a pre-quoted token — wrap it in quotes
within `reload.cmd` if it may contain spaces:

```toml
[reload]
cmd = 'dmut apply "{pg.uri}" /migrations'
timeout = 120        # seconds ; reload.cmd is aborted and considered failed past this
```

A literal `{`/`}` anywhere in `reload.cmd` is always parsed as a placeholder attempt — there is
no escape syntax. A command that needs a literal brace should produce it at runtime (an
environment variable, a here-string) rather than write it directly into `reload.cmd`.

The spawned process receives the full parent environment, unmodified, in addition to whatever
was substituted into the command line — this is what makes Docker/Kubernetes/Swarm secret
injection work for `reload.cmd` : put the secret in an env var the orchestrator already injects,
and reference it as `{THAT_VAR}` (or let the process read it directly, since the full
environment is passed through either way). It is your own responsibility not to expose secrets
through the config-key side of interpolation — every resolved configuration value is available
to it.

## Output and failure handling

`reload.cmd`'s stdout and stderr are logged line by line, tagged `component=reload.cmd` and
`stream=stdout`/`stream=stderr`. A line that parses as a JSON object has its own keys merged
into the log record on top of those two, for tools that support structured output.

If `reload.cmd` exits non-zero or exceeds `reload.timeout`, rel skips reintrospecting the schema
for that reload — but still rebuilds the well-known query registry and [Jet
template](../http/requests-responses.md#rendering-html-with-a-template) cache, against the
previous, still-loaded schema, so a well-known-query or template change on disk still takes
effect even when the migration step itself failed. A reintrospection, `/route` registry, or mux
build failure past that point leaves the previous schema, registry, and templates running
untouched — the same "log and continue" behavior throughout.

## Using dmut

[dmut](https://github.com/ceymard/dmut) — a dependency-graph migration tool, not a sequential
up/down list : each mutation file declares what it depends on, and changing one recursively
downs and re-ups it and everything that depends on it — is well suited to this "runs on every
reload" model, since a run against an already-applied schema is a cheap no-op. A typical setup:

```toml
[reload]
cmd = 'dmut apply "{pg.uri}" /migrations'
```

This repo's own dev database uses it locally :

```sh
just test-db-migrate   # dmut apply against the dev database
just test-db-fresh     # tear down, bring up Postgres, migrate, and seed in one shot
```
