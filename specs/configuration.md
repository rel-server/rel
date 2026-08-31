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

Rules:

- Works identically across all three sources — a TOML value, an env var, and a CLI flag can each use `$FILE$`.
- Resolved once, over the fully merged configuration tree, before any type coercion or validation.
- Exactly one trailing `\n` or `\r\n` is trimmed from the file's contents; nothing else is altered, so multi-line values (private keys, certificates) survive intact.
- The path is resolved relative to the process's current working directory.
- An unreadable file, with no `$DEFAULT$` given, is a fatal, startup-terminating error.
- An empty file is a valid value (an empty string) — never treated as "not found."
- `$FILE$` always produces exactly one string value; it never parses the referenced file's structure. `$FILE$` is not limited to secrets — it may hold any scalar value that's more convenient to keep in its own file.

## No arrays

Configuration MUST NOT contain arrays, in any source, and the loader enforces this rather than merely documenting it:

- A set of *named* things (multiple SAML IdPs, multiple OpenID providers) is modeled as named keys, never a list — e.g. `REL_OPENID__GOOGLE__SECRET`, `REL_OPENID__SALESFORCE__SECRET`.
- A list of plain, unnamed scalars (e.g. allowed CORS origins) is a single comma-separated string, trimmed and filtered of empty entries by the accessor that reads it — the same convention legacy used for this case.
- After loading, the config package walks the fully merged tree and rejects (fatal error) any value that is a genuine array/list — e.g. a literal TOML/YAML array — rather than accepting or silently flattening it.

## Error handling and secrets

All configuration errors (missing required value, malformed value, unreadable `$FILE$` path, type mismatch, ambiguous config file, ...) MUST be logged. A logged error MUST identify the offending option path, but MUST NEVER include the resolved value — for every option, not only ones that are obviously secrets, since `$FILE$` is general-purpose and the loader has no reliable way to know which values are sensitive.

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

`dev` (top-level, bool, default `false`) : gates the extra detail `error-handling.md ## Postgres error detail` and `## Stack traces` add to error responses — full Postgres error text and a stack trace, both otherwise omitted. Off by default so a deployment that never explicitly opts in never risks leaking either. See those two sections for exactly what `dev: true` changes ; this key exists purely to gate them; it has no other effect (in particular it does NOT change the logging level — `logging.md ## Configuration`'s `logging.level` stays the one and only control for that, kept orthogonal deliberately, since a deployment might want dev-mode error detail without also wanting DEBUG-level log volume, or vice versa).

## Immutability

Configuration is static for the lifetime of the process — there is no config reload; changing configuration means relaunching the process. The one exception is the database schema cache, which may be refreshed at runtime independently of configuration, and is not part of the config system described here.
