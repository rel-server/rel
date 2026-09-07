---
icon: material/cog
---

# Configuration

Every setting has a dotted name — `jwt.max_age`, `http.cors.allowed_origins` — and can come
from three places, later ones overriding earlier ones for the same key:

1. A **config file** (TOML, YAML, or HUML) — lowest precedence.
2. **Environment variables**, prefixed `REL_`, with `__` marking each level of nesting:
   `REL_HTTP__CORS__ALLOWED_ORIGINS`.
3. **Command-line flags** — highest precedence, dotted directly: `--http.cors.allowed_origins`.

The same key, three ways:

```toml
# rel.toml
[pg]
uri = "postgres://user:pass@localhost:5432/mydb"

[http]
port = 8080
```

```sh
REL_PG__URI="postgres://user:pass@localhost:5432/mydb" REL_HTTP__PORT=8080 rel
```

```sh
rel --pg.uri "postgres://user:pass@localhost:5432/mydb" --http.port 8080
```

Mix and match freely — the common pattern is a config file for anything that doesn't change
between environments, environment variables for anything that does (secrets, connection
strings), and flags for one-off overrides.

See [Best practices](best-practices.md) for a hardening checklist once you're past a first
local run and setting these for a real deployment.

## Config file discovery

Point rel at a file explicitly with `--config <path>` (or `REL_CONFIG=<path>`); an unreadable
or malformed file at that path is a fatal, startup-terminating error.

Without `--config`, rel searches, in order, and loads the first file it finds:

1. `./rel.toml`, `./rel.yaml`, `./rel.yml`, `./rel.huml`
2. `$XDG_CONFIG_HOME/rel/rel.{toml,yaml,yml,huml}` (`$XDG_CONFIG_HOME` defaults to `~/.config`)
3. `/etc/rel/rel.{toml,yaml,yml,huml}`

Finding more than one recognized file at the same step (`rel.toml` *and* `rel.yaml` in the
same directory, say) is a fatal error — rel never picks one silently. Finding no file at any
step isn't an error either; rel runs on environment variables, flags, and built-in defaults
alone, which is enough to start it (see [Getting started](../getting-started/index.md)).

## Secrets and generated values

Any single config value, from any of the three sources, can point at a file instead of being
written literally:

```toml
[jwt]
secret = "$FILE$/secrets/jwt-secret:./jwt-secret$GEN$32"
```

- `$FILE$/path` — read the value from that file (exactly one trailing newline trimmed;
  otherwise byte-for-byte, so a multi-line certificate survives intact).
- `$FILE$/path$DEFAULT$fallback` — read the file if it exists, otherwise use `fallback`
  literally.
- `$FILE$/path$GEN$32` — read the file if it exists, otherwise generate 32 random characters,
  write them to `path`, and use that.

`path` can itself be a colon-separated list of candidates, tried in order — that's what
`jwt.secret`'s own default (above) does: prefer `/secrets/jwt-secret` (a mounted volume in a
real deployment), fall back to `./jwt-secret` for a local run. For `$GEN$` specifically, rel
tries every candidate for an *existing* value first; only if none exists does it generate into
the first candidate whose parent directory exists; and only if no candidate's directory exists
at all does it generate an ephemeral, in-memory value for that run alone — logged loudly at
`WARN`, since it silently invalidates every session on the next restart. That last case is
fine for a quick local run and wrong for anything that needs sessions to survive a restart.

## No arrays

No config value, from any source, is ever a list. A set of *named* things becomes named keys
— several OpenID providers are `openid.google.*`, `openid.okta.*`, not an array entry each.
A list of plain scalars — CORS origins, for instance — is one comma-separated string, split
and trimmed at the point it's read.

See [Configuration reference](reference.md) for the exhaustive key list.

## Development mode

`dev` (default `false`) adds the real Postgres error text and a stack trace to an otherwise
generic error response. It has no effect on log verbosity — that's `logging.level` alone, set
independently so a deployment can want one without the other.

## Nothing reloads

Configuration is fixed for the life of the process — change a setting, restart rel. The one
exception is the database schema itself, which rel re-introspects on a `SIGUSR1` signal
without a restart; see [Operations](operations.md).
