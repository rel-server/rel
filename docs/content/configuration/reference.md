---
icon: material/notebook
---

# Configuration reference

Every key below can be set as `REL_<SECTION>__<KEY>` (uppercased, `.` → `__`) or `--<key>`,
equally. This is every configuration key rel understands, including the dynamic,
parametrized ones (`openid.<name>.*` and the like) — `rel --help` prints the same list from
the binary itself, if you want it without leaving a terminal.

## General

| Key | Default | What it does |
|---|---|---|
| `dev` | `false` | Adds the real Postgres error text and a stack trace to an otherwise generic error response. See [Development mode](index.md#development-mode). |

## Postgres connection

| Key | Default | What it does |
|---|---|---|
| `pg.uri` | — | Full `postgres://user:pass@host:port/db`. Takes precedence when set — populates `pg.host`/`pg.port`/`pg.database` itself ; setting them alongside `pg.uri` is a configuration error. |
| `pg.host` / `pg.port` / `pg.database` / `pg.user` / `pg.password` | `localhost` / `5432` / — / — / — | Granular connection fields, used only when `pg.uri` is unset. |
| `pg.pool_size` | `10` | Max connections in the pool serving requests. Startup introspection and migrations each use one short-lived connection regardless. |
| `pg.query.user` / `pg.query.password` | = `pg.user`/`pg.password` | An optional, narrower-scoped login for serving requests specifically — introspection and migrations still use `pg.user`. |
| `pg.query.anonymous_role` | `~anonymous` | The role a request with no valid session runs as. See [Authentication](../http/authentication.md). |
| `pg.query.max_depth` | `6` | Maximum join nesting a query may specify. |
| `pg.query.wellknown_path` | `/wellknown` | Colon-separated directories, searched recursively for well-known query files. See [Well-known queries](../query-language/well-known-queries.md). |

## HTTP server

| Key | Default | What it does |
|---|---|---|
| `http.host` / `http.port` | all interfaces / `8080` | Listen address. |
| `http.public_host` | disabled | This deployment's externally-reachable domain (bare host, no scheme/port) — required for OpenID/SAML redirect URLs to resolve. See [Authentication](../http/authentication.md). |
| `http.cookies_max_age` | `86400` (seconds) | Default max-age for a cookie set via a route response, when the response doesn't specify one. Doesn't apply to the JWT cookie — see `jwt.max_age` below. |
| `http.max_body_size` | 10 MiB | Hard cap on a declared route request's entire body (multipart envelope included). Rejected with `413` before any of it is buffered. |
| `http.max_upload_size` | = `http.max_body_size` | Hard cap on a `stream_upload` route's streamed payload — separate from `http.max_body_size` since this path streams to disk instead of buffering in memory. See [File uploads](../http/uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `http.max_part_count` | `100` | Max number of `multipart/form-data` parts a single declared-route request may contain. |
| `http.static.path` | — | Colon-separated directories served at the router root, as the fallback for any path no declared route claims. See [Static files](../http/static-files.md). |
| `http.templates.path` | `/template` | Directory Jet templates are loaded from, for a route's or middleware's own `template`. See [Requests and responses](../http/requests-responses.md#rendering-html-with-a-template). |
| `http.typescript.enable` | `false` (`true` in dev) | Serve `GET /rel/database.ts`. See [TypeScript client](../typescript-client.md). |
| `http.typescript.schemas` | every schema but `pg_catalog` | Comma-separated whitelist of schemas `GET /rel/database.ts` may export; intersected with that request's own `?schemas=` param. |

## Route declaration and gating

| Key | Default | What it does |
|---|---|---|
| `route.<schema>.<function>.path` | — (required) | Declares `<schema>.<function>` routable at this chi-syntax path. See [HTTP routes](../http/index.md#declaring-a-route). |
| `route.<schema>.<function>.method` | inferred | Comma-separated accepted methods. |
| `route.<schema>.<function>.template` | unset | Default Jet template, used when the response doesn't set its own. |
| `route.<schema>.<function>.stream_upload` | `false` | Flags the two-call disk-streaming upload flow. See [File uploads](../http/uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `route.<schema>.<function>.middleware` | `false` | Flags a middleware function, run ahead of every route/`/rel`/static file under its own path prefix. See [HTTP routes](../http/index.md#middleware). |
| `http.functions.allowed_auth` | unrestricted | Regexp restricting which route/middleware functions may mint or clear a session (set `jwt` on their response). See [Authentication](../http/authentication.md). |
| `http.functions.sso_callback` | disabled | Fallback callback function an `openid.<name>`/`saml.<name>` entry uses when it doesn't set its own `callback_function`. See [Authentication](../http/authentication.md). |

## CORS

| Key | Default | What it does |
|---|---|---|
| `http.cors.allowed_origins` | empty (closed) | Comma-separated exact origins allowed cross-origin, or the literal `*`. |
| `http.cors.allowed_methods` | `GET, POST, PUT, PATCH, DELETE, OPTIONS` | Methods a CORS preflight may approve. |
| `http.cors.allowed_headers` | a sensible default set | Request headers a CORS preflight may approve. |
| `http.cors.max_age` | `600` (seconds) | How long a browser may cache one preflight response. |

## Content-Security-Policy

| Key | Default | What it does |
|---|---|---|
| `http.csp.default_src` | `'self'` | The baseline CSP directive, applied unless a more specific one below overrides it. |
| `http.csp.<directive>`, one of `script_src`, `style_src`, `img_src`, `font_src`, `connect_src`, `object_src`, `frame_ancestors`, `base_uri`, `form_action` | falls back to `default_src` | Overrides that one directive specifically. |
| `http.csp.policy` | unset | The full, raw `Content-Security-Policy` header value — replaces every individual `http.csp.*` directive above entirely when set. |

See [CORS and CSP](../http/cors-csp.md) for how these compose with a
per-response nonce and a route's own `HttpResponse.csp` override.

## Sessions (JWT)

| Key | Default | What it does |
|---|---|---|
| `jwt.secret` | auto-generated | HMAC signing secret — see [Secrets and generated values](index.md#secrets-and-generated-values) above. |
| `jwt.cookie_name` | `accesstoken` | Name of the cookie carrying the JWT. |
| `jwt.algorithm` | `HS256` | `HS256`, `HS384`, or `HS512`. |
| `jwt.same_site` | `Lax` | `SameSite` attribute of the JWT cookie: `Strict`, `Lax`, or `None`. |
| `jwt.max_age` | `1800` (30 min) | How long a freshly-minted token stays valid. |
| `jwt.renew_after` | `0.5` | Fraction of a token's own lifespan elapsed before it's renewed on next use. |
| `jwt.max_session_age` | `604800` (7 days) | Hard ceiling on a session's total lifetime, regardless of renewal. |

Full session lifecycle — minting, renewal, revocation via middleware — is [Authentication](../http/authentication.md).

## SAML (shared SP identity)

| Key | Default | What it does |
|---|---|---|
| `saml.certificate_path` | generated on first boot | Colon-separated search list for this deployment's SAML SP certificate, shared across every `saml.<name>` entry. |
| `saml.private_key_path` | generated on first boot | Search list for that certificate's private key, same rule as `saml.certificate_path`. |

## OpenID Connect and SAML providers

One named entry per provider — `<name>` is yours to choose, unrelated to the provider's brand.
See [Authentication](../http/authentication.md) for the full picture (routes, callback function
contract, worked examples); this is the exhaustive key list.

| Key pattern | Default | What it does |
|---|---|---|
| `openid.<name>.issuer` | — (required) | The OIDC issuer URL; `/.well-known/openid-configuration` is discovered from it. |
| `openid.<name>.client_id` / `openid.<name>.client_secret` | `$FILE$/secrets/openid-<name>.id` / `.secret` | Credentials issued by the IdP when this app was registered with it. |
| `openid.<name>.scopes` | `openid, email, profile` | Comma-separated OIDC scopes requested. |
| `openid.<name>.fetch_userinfo` | `false` | Also call the userinfo endpoint after token exchange, merging its claims over the ID token's own. |
| `openid.<name>.callback_function` | falls back to `http.functions.sso_callback` | Function invoked with the verified claims. |
| `openid.<name>.public_host` | falls back to `http.public_host` | Overrides the deployment's public host for this provider entry alone. |
| `saml.<name>.idp_metadata_url` | — (required) | Fetched once at startup; retried lazily on the next login attempt if it failed. |
| `saml.<name>.force_signed_requests` | `true` | Sign the outgoing `AuthnRequest` with the SP key. |
| `saml.<name>.callback_function` | falls back to `http.functions.sso_callback` | Same as `openid.<name>.callback_function`. |
| `saml.<name>.public_host` | falls back to `http.public_host` | Same as `openid.<name>.public_host`. |

## Logging

| Key | Default | What it does |
|---|---|---|
| `logging.handler` | `pretty` | `pretty` or `json`. |
| `logging.level` | `info` | `debug`, `info`, `warn`, or `error`. |
| `logging.filter.<attr>` | — | A regexp a log line's `<attr>` must match to be shown at all; a line with nothing to filter against for that key is shown regardless. |
| `logging.exclude.<attr>` | — | Same shape, inverted — suppresses a matching line. Applied after any `logging.filter.*` also set. |

## Restricting what a query can reach

Two dynamic namespaces add to (never replace) rel's built-in blacklist:

| Key pattern | Default | What it does |
|---|---|---|
| <code>blacklist.relations.&lt;schema&gt;.&lt;name&#124;*&gt;</code> | see below | `y`/`n`; `*` blacklists an entire schema. `pg_catalog.*` and `information_schema.*` are blacklisted by default. |
| <code>blacklist.functions.&lt;schema&gt;.&lt;name&#124;*&gt;</code> | see below | `y`/`n`; `*` blacklists an entire schema. A handful of individually dangerous functions (`pg_sleep`, `pg_terminate_backend`, the `pg_advisory_*lock*` family, ...) are blacklisted by default. |

```toml
[blacklist.relations.public]
orders = "y"

[blacklist.functions.pg_catalog]
dblink = "y"
```

## Reload

| Key | Default | What it does |
|---|---|---|
| `reload.cmd` | — | Command line rel runs before every reload (startup and every `SIGUSR1`). Empty skips this step entirely. See [Reload](reload.md) for the interpolation syntax. |
| `reload.timeout` | `120` (seconds) | `reload.cmd` is aborted and considered failed once this elapses. |
| `reload.drain_timeout` | `30` (seconds) | How long a reload waits for in-flight requests to finish, before `reload.cmd` runs, before cancelling their contexts and proceeding anyway. |

## TypeScript export

| Key | Default | What it does |
|---|---|---|
| `typescript.helper_path` | disabled | Filesystem path rel (re)writes `database.ts`'s content to directly, at startup and on every `SIGUSR1` reload — for an editor/LSP watching a real file on disk. See [TypeScript client](../typescript-client.md). |
