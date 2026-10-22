---
icon: material/database-sync
---

# Migrations (dmut)

rel embeds [dmut](https://github.com/ceymard/dmut), a dependency-graph migration tool : each
mutation file declares what it depends on, and changing one recursively downs and re-ups it and
everything that depends on it. A run against an already-applied schema does nothing, which suits
a step that runs on every reload.

## When it runs

Set `dmut.path` and rel applies your mutations at every startup, before it first introspects the
schema, and again on every `SIGUSR1`. It runs inside rel's own process, just before
[`reload.cmd`](reload.md), which still runs afterwards if you set it.

```toml
[dmut]
path = "/sql"          # colon-separated : "/sql:/extra-sql"
timeout = 120          # seconds, for the whole run
```

`dmut.path` empty (the default) skips this step entirely. A missing directory is an error, not a
silent no-op.

## Login

By default dmut connects with the same login as the rest of rel (`pg.user`/`pg.password`, on
`pg.host`/`pg.port`/`pg.database`, or taken from `pg.uri`). Migrations often need more rights than the
login that serves requests ; give dmut its own :

```toml
[dmut]
path = "/sql"
user = "migrator"
password = "$FILE$/run/secrets/migrator_password"
```

`dmut.user` and `dmut.password` fall back independently : leaving `password` unset sends
`pg.password`. Host, port, database and connection parameters always come from `pg`.

## Modes

`dmut.mode` picks how a change to existing `core` statements (tables and other objects that hold
data) is applied. A revision file's own `mode:` overrides it.

| Mode | Behaviour |
|---|---|
| `default` | Downs the changed statement and everything after it in its mutation, then re-ups. If that fails, the whole run is rolled back. |
| `simple` | Refuses to run when `core` has any change that isn't a pure append. Use it where a migration must never be destructive. |
| `full` | Tries `default` first. If that fails, rolls back to before the attempt, downs the changed mutations and everything depending on them, rebuilds them from scratch, and tries once more. |

## Failure handling

The run happens in a transaction : a failure rolls everything back and leaves the schema as it was.
rel logs the error and carries on, as it does for `reload.cmd`. At startup it serves the schema as it
finds it ; on a `SIGUSR1` reload it skips reintrospection (see [Reload](reload.md#output-and-failure-handling)).
`reload.cmd` still runs if dmut failed, and the other way round.

## Logs

dmut's records go through rel's own logger with `module=dmut`, so `logging.filter` and
`logging.exclude` apply to them like to any other module. Set `logging.level = "debug"` to see every
SQL statement. Passwords are never logged.

## Writing mutations

See the [dmut documentation](https://github.com/ceymard/dmut) for the file format.
This repo's `boot/testdata/dmut/` holds a small example.
