# Configuration

Throughout the specifications, option variables are always referred to in their dotted form, e.g. `logging.handler`. Each source below translates that dotted form into its own natural syntax; internally, the merged configuration tree is always dot-delimited.

## Sources and precedence

Rel merges configuration from three kinds of sources using `github.com/knadh/koanf`. Later sources override earlier ones for the same key:

1. **Config file** (TOML, YAML, or HUML) — lowest precedence. See "Config file discovery" below.
2. **Environment variables** — every variable is prefixed `REL_`; `__` is the object delimiter (`REL_OPENID__GOOGLE__SECRET=...` → `openid.google.secret`).
3. **Command-line flags** — highest precedence. Dotted form directly: `--logging.handler=value` or `--logging.handler value` → `logging.handler`.

## Config file discovery

- `--config <path>` / `-c <path>` (equivalently `REL_CONFIG=<path>`) loads exactly that file. If it can't be read or fails to parse, this is a fatal, startup-terminating error.
- If neither is given, Rel searches the following locations, in order, and loads the first one found:
  1. `./rel.toml`, `./rel.yaml`, `./rel.yml`, `./rel.huml`
  2. `$XDG_CONFIG_HOME/rel/rel.{toml,yaml,yml,huml}` (`$XDG_CONFIG_HOME` defaults to `~/.config` if unset)
  3. `/etc/rel/rel.{toml,yaml,yml,huml}`
- If more than one recognized file exists at the same search step (e.g. both `rel.toml` and `rel.yaml` in `/etc/rel/`), this is a fatal configuration error. Ambiguity is never resolved silently by picking one.
- If no path was given explicitly and none of the search locations contain a file, that is *not* an error — Rel proceeds on environment variables, CLI flags, and built-in defaults alone.

## `$FILE$` value indirection

Any scalar configuration value, from any source, may be written as:

- `$FILE$/absolute/or/relative/path` — replaced with the contents of the file at that path.
- `$FILE$/path$DEFAULT$fallback value` — replaced with the file's contents if it exists, or `fallback value` if it doesn't.
- `$FILE$/path$GEN$128` - replaced with the file contents if it exists, or base32 random characters (here, 128 characters) which are then written to the path

The path portion of any of the three forms above may be a colon-separated list of candidate paths, tried in order — the same search-list convention `http.static.path`/`pg.query.wellknown_path` use.

Rules:

- Works identically across all three sources — a TOML value, an env var, and a CLI flag can each use `$FILE$`.
- Resolved once, over the fully merged configuration tree, before any type coercion or validation.
- Exactly one trailing `\n` or `\r\n` is trimmed from the file's contents; nothing else is altered, so multi-line values (private keys, certificates) survive intact.
- Each candidate path is resolved relative to the process's current working directory.
- An empty file is a valid value (an empty string) — never treated as "not found."
- `$FILE$` always produces exactly one string value; it never parses the referenced file's structure. `$FILE$` is not limited to secrets — it may hold any scalar value that's more convenient to keep in its own file.

### Plain and `$DEFAULT$` path lists

- Each candidate is tried in order ; the first one that reads successfully is used.
- If no candidate reads successfully, `$DEFAULT$` falls back to its given value.
- If no candidate reads successfully and no `$DEFAULT$` is given, this is a fatal, startup-terminating error.

### `$GEN$` multi-path resolution

`$GEN$`'s candidate list is resolved in three passes, each considering every candidate in list order before moving to the next pass — an earlier candidate always wins over a later one, regardless of which pass resolves it.

1. The first candidate whose file already exists and reads successfully is used as-is. If an existing candidate's value doesn't match the declared length, this is a fatal, startup-terminating error naming the mismatched candidate.
2. If no candidate has an existing value, the first candidate whose PARENT DIRECTORY exists is generated into and used. A candidate whose parent directory doesn't exist is skipped, not an error. A candidate whose parent directory exists but still can't be written to (permissions, read-only filesystem, ...) is a fatal, startup-terminating error ; resolution does not continue to the next candidate.
3. If no candidate's parent directory exists, an ephemeral, in-memory-only value is generated for this run alone (never written to disk) — a new one on every restart. This is logged at `WARN`, including the generated value itself, formatted to be hard to miss in the log output.

> Why: pass 2's directory-exists-but-write-fails case is deliberately never a reason to fall through — it means an operator went to the trouble of creating that directory, so a write failure there is a real misconfiguration to surface loudly, not a signal to look elsewhere.

> Why: pass 3's ephemeral fallback exists purely so a developer can run rel with no volumes/paths configured at all, without it refusing to boot. It is unsuitable for any deployment that needs sessions/tokens to survive a restart.

## No arrays

Configuration MUST NOT contain arrays, in any source, and the loader enforces this rather than merely documenting it:

- A set of *named* things (multiple SAML IdPs, multiple OpenID providers) is modeled as named keys, never a list — e.g. `REL_OPENID__GOOGLE__SECRET`, `REL_OPENID__SALESFORCE__SECRET`.
- A list of plain, unnamed scalars (e.g. allowed CORS origins) is a single comma-separated string, trimmed and filtered of empty entries by the accessor that reads it — the same convention legacy used for this case.
- After loading, the config package walks the fully merged tree and rejects (fatal error) any value that is a genuine array/list — e.g. a literal TOML/YAML array — rather than accepting or silently flattening it.

## Error handling and secrets

All configuration errors (missing required value, malformed value, unreadable `$FILE$` path, type mismatch, ambiguous config file, ...) MUST be logged. A logged error MUST identify the offending option path, but MUST NEVER include the resolved value — for every option, not only ones that are obviously secrets, since `$FILE$` is general-purpose and the loader has no reliable way to know which values are sensitive.

This never-log-the-value rule applies to errors only. `$GEN$` multi-path resolution's pass 3 (`## $FILE$ value indirection ### $GEN$ multi-path resolution`) is a deliberate, sole exception : it is not an error, and the generated value is logged in full at `WARN` on purpose, since it's otherwise never visible anywhere and would silently regenerate — and invalidate every session/token issued against it — on every restart.

## Accessing configuration

A `config` package merges all sources as described above into a koanf instance, then exposes it through a `*ConfigReader`:

```go
conf.GetObject(path string) (*ConfigReader, error)
conf.GetIterator(path string) (iter.Seq2[string, *ConfigReader], error)
conf.GetString(path string) (string, error)
conf.GetInt(path string) (int, error)
conf.GetStrings(path string) ([]string, error) // comma-separated convention, see "No arrays"
// ... one pair of accessors per supported type

// OrDefault variants return the default both when the key is absent and
// when retrieval otherwise errored, letting each call site decide whether
// a missing/bad value is fatal or just "use the default."
conf.GetObjectOrDefault(path string, def func() *ConfigReader) *ConfigReader
conf.GetStringOrDefault(path string, def string) string
// ...
```

- `GetObject` and `GetIterator` scope to a sub-path. Calling either on a path that isn't an object is an error, not an empty result.
- `GetIterator` yields entries in a deterministic (alphabetically sorted) order.
- Type accessors (`GetString`, `GetInt`, ...) enforce the declared type on top of koanf's own untyped storage: a value that's present but the wrong type is an error, never silently coerced or zeroed.
- Every retrieval error is logged per the rule above — path only, never the value.

## The assembled `Config` object

Once all sources are merged, Rel builds one fully-typed `Config` struct from the `*ConfigReader` tree, once, at startup. This is the only config object the rest of the server touches — application code reads `Config` fields, not `conf.Get*`, outside of this assembly step.

Building `Config` is where validation happens: every field is populated (or defaulted) from the reader, and every error along the way — missing required value, wrong type — is collected rather than raised immediately. If any errors were collected, Rel logs all of them together and exits, so a misconfigured deployment gets one complete list of everything wrong instead of discovering issues one restart at a time.

The struct below is an illustrative sketch of the shape, not an authoritative contract — treat the actual Go source as ground truth if the two ever disagree.

```go
type Config struct {
    Hostname string
    OpenID   map[string]OpenIDEndpoint
    SAML     map[string]SAMLEndpoint
    // ...
}

type OpenIDEndpoint struct {
    Key    string
    Secret string
    // ...
}
```

## Development mode

`dev` (top-level, bool, default `false`) : gates the extra detail `error-handling.md ## Postgres error detail` and `## Stack traces` add to error responses — full Postgres error text and a stack trace, both otherwise omitted. It has no other effect. It does NOT change the logging level; `logging.md ## Configuration`'s `logging.level` is the only control for that.

> Why: off by default so a deployment never leaks either kind of detail without opting in. Kept orthogonal from `logging.level` since a deployment may want dev-mode error detail without DEBUG-level log volume, or vice versa.

## Immutability

Configuration is static for the lifetime of the process — there is no config reload; changing configuration means relaunching the process. The one exception is the database schema cache, which may be refreshed at runtime independently of configuration, and is not part of the config system described here.
