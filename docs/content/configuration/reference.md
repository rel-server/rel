---
icon: material/notebook
---

# Configuration reference

Every key below can be set as `REL_<SECTION>__<KEY>` (uppercased, `.` → `__`) or `--<key>`,
equally. This is every configuration key rel understands, including the dynamic,
parametrized ones (`openid.<name>.*` and the like) — `rel --help` prints the same list from
the binary itself, if you want it without leaving a terminal.

## General

| Key | Env var | Default | What it does |
|---|---|---|---|
| `dev` | `REL_DEV` | `false` | Adds the real Postgres error text and a stack trace to an otherwise generic error response. See [Development mode](index.md#development-mode). |

## Postgres connection

| Key | Env var | Default | What it does |
|---|---|---|---|
| `pg.uri` | `REL_PG__URI` | derived from the granular fields below when unset | Full `postgres://user:pass@host:port/db`. Takes precedence when set — populates `pg.host`/`pg.port`/`pg.database`/`pg.user`/`pg.password` itself ; setting any of them alongside `pg.uri` is a configuration error. When `pg.uri` isn't set, it's built from the granular fields instead (defaults included), so it's never left blank. |
| `pg.host` / `pg.port` / `pg.database` / `pg.user` / `pg.password` | `REL_PG__HOST` / `REL_PG__PORT` / `REL_PG__DATABASE` / `REL_PG__USER` / `REL_PG__PASSWORD` | `localhost` / `5432` / — / — / — | Granular connection fields, used only when `pg.uri` is unset — otherwise derived from `pg.uri` itself. |
| `pg.pool_size` | `REL_PG__POOL_SIZE` | `10` | Max connections in the pool serving requests. Startup introspection uses one short-lived connection regardless. |
| `pg.query.anonymous_role` | `REL_PG__QUERY__ANONYMOUS_ROLE` | `~anonymous` | The role a request with no valid session runs as. See [Authentication](../http/authentication.md). |
| `pg.query.max_depth` | `REL_PG__QUERY__MAX_DEPTH` | `6` | Maximum join nesting a query may specify. |
| `pg.query.wellknown_path` | `REL_PG__QUERY__WELLKNOWN_PATH` | `/wellknown` | Colon-separated directories, searched recursively for well-known query files. See [Well-known queries](../query-language/well-known-queries.md). |
| `pg.query.allow_count` | `REL_PG__QUERY__ALLOW_COUNT` | same as `dev` | Enables `ComplexQuery`'s `count` flag. See [Complex queries](../query-language/complex-query.md#availability). |
| `pg.query.allow_stats` | `REL_PG__QUERY__ALLOW_STATS` | same as `dev` | Enables `ComplexQuery`'s `stats` flag. |
| `pg.query.allow_query_plan` | `REL_PG__QUERY__ALLOW_QUERY_PLAN` | same as `dev` | Enables `ComplexQuery`'s `query_plan` flag. |
| `pg.query.allow_sql` | `REL_PG__QUERY__ALLOW_SQL` | same as `dev` | Enables `ComplexQuery`'s `sql` flag. |
| `pg.query.allow_rollback` | `REL_PG__QUERY__ALLOW_ROLLBACK` | same as `dev` | Enables `ComplexQuery`'s `rollback` flag ; ungranted is a hard error, not a silent degrade. |

## HTTP server

| Key | Env var | Default | What it does |
|---|---|---|---|
| `http.host` / `http.port` | `REL_HTTP__HOST` / `REL_HTTP__PORT` | all interfaces / `8080` | Listen address. |
| `http.public_host` | `REL_HTTP__PUBLIC_HOST` | disabled | This deployment's externally-reachable domain (bare host, no scheme/port) — required for OpenID/SAML redirect URLs to resolve. See [Authentication](../http/authentication.md). |
| `http.cookies_max_age` | `REL_HTTP__COOKIES_MAX_AGE` | `86400` (seconds) | Default max-age for a cookie set via a route response, when the response doesn't specify one. Doesn't apply to the JWT cookie — see `jwt.max_age` below. |
| `http.max_body_size` | `REL_HTTP__MAX_BODY_SIZE` | 10 MiB | Hard cap on a declared route request's entire body (multipart envelope included). Rejected with `413` before any of it is buffered. |
| `http.max_part_count` | `REL_HTTP__MAX_PART_COUNT` | `100` | Max number of `multipart/form-data` parts a single declared-route request may contain. |
| `http.static.path` | `REL_HTTP__STATIC__PATH` | — | Colon-separated directories served at the router root, as the fallback for any path no declared route claims. See [Static files](../http/static-files.md). |
| `http.upload.dir` | `REL_HTTP__UPLOAD__DIR` | unset (uploads disabled) | Subpath of the static write directory (the first entry of `http.static.path`) that every `stream_upload` writes under. Uploaded files stay servable as static content, but no file under here is ever eligible for jet execution. See [File uploads](../http/uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `http.upload.max_size` | `REL_HTTP__UPLOAD__MAX_SIZE` | = `http.max_body_size` | Hard cap on a `stream_upload` route's streamed payload — separate from `http.max_body_size` since this path streams to disk instead of buffering in memory. See [File uploads](../http/uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `http.templates.path` | `REL_HTTP__TEMPLATES__PATH` | `/template` | Directory Jet templates are loaded from, for a route's or middleware's own `template`, and (merged with `http.static.path`) for a static `.jet` render. See [Requests and responses](../http/requests-responses.md#rendering-html-with-a-template) and [Static files](../http/static-files.md). |
| `http.typescript.enable` | `REL_HTTP__TYPESCRIPT__ENABLE` | `false` (`true` in dev) | Serve `GET /rel/database.ts`. See [TypeScript client](../typescript/index.md). |
| `http.typescript.schemas` | `REL_HTTP__TYPESCRIPT__SCHEMAS` | every schema but `pg_catalog` | Comma-separated whitelist of schemas `GET /rel/database.ts` may export; intersected with that request's own `?schemas=` param. |

## Route declaration and gating

| Key | Env var | Default | What it does |
|---|---|---|---|
| `route.<schema>.<function>.path` | `REL_ROUTE__<SCHEMA>__<FUNCTION>__PATH` | — (required) | Declares `<schema>.<function>` routable at this chi-syntax path. See [HTTP routes](../http/index.md#declaring-a-route). |
| `route.<schema>.<function>.method` | `REL_ROUTE__<SCHEMA>__<FUNCTION>__METHOD` | inferred | Comma-separated accepted methods. |
| `route.<schema>.<function>.template` | `REL_ROUTE__<SCHEMA>__<FUNCTION>__TEMPLATE` | unset | Default Jet template, used when the response doesn't set its own. |
| `route.<schema>.<function>.stream_upload` | `REL_ROUTE__<SCHEMA>__<FUNCTION>__STREAM_UPLOAD` | `false` | Flags the two-call disk-streaming upload flow. See [File uploads](../http/uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `route.<schema>.<function>.middleware` | `REL_ROUTE__<SCHEMA>__<FUNCTION>__MIDDLEWARE` | `false` | Flags a middleware function, run ahead of every route/`/rel`/static file under its own path prefix. See [HTTP routes](../http/index.md#middleware). |
| `http.functions.allowed_auth` | `REL_HTTP__FUNCTIONS__ALLOWED_AUTH` | unrestricted | Regexp restricting which route/middleware functions may mint or clear a session (set `jwt` on their response). See [Authentication](../http/authentication.md). |
| `http.functions.sso_callback` | `REL_HTTP__FUNCTIONS__SSO_CALLBACK` | disabled | Fallback callback function an `openid.<name>`/`saml.<name>` entry uses when it doesn't set its own `callback_function`. See [Authentication](../http/authentication.md). |

## CORS

| Key | Env var | Default | What it does |
|---|---|---|---|
| `http.cors.allowed_origins` | `REL_HTTP__CORS__ALLOWED_ORIGINS` | empty (closed) | Comma-separated exact origins allowed cross-origin, or the literal `*`. |
| `http.cors.allowed_methods` | `REL_HTTP__CORS__ALLOWED_METHODS` | `GET, POST, PUT, PATCH, DELETE, OPTIONS` | Methods a CORS preflight may approve. |
| `http.cors.allowed_headers` | `REL_HTTP__CORS__ALLOWED_HEADERS` | a sensible default set | Request headers a CORS preflight may approve. |
| `http.cors.max_age` | `REL_HTTP__CORS__MAX_AGE` | `600` (seconds) | How long a browser may cache one preflight response. |

## Content-Security-Policy

| Key | Env var | Default | What it does |
|---|---|---|---|
| `http.csp.default_src` | `REL_HTTP__CSP__DEFAULT_SRC` | `'self'` | The baseline CSP directive, applied unless a more specific one below overrides it. |
| `http.csp.<directive>`, one of `script_src`, `style_src`, `img_src`, `font_src`, `connect_src`, `object_src`, `frame_ancestors`, `base_uri`, `form_action` | `REL_HTTP__CSP__<DIRECTIVE>` | falls back to `default_src` | Overrides that one directive specifically. |
| `http.csp.policy` | `REL_HTTP__CSP__POLICY` | unset | The full, raw `Content-Security-Policy` header value — replaces every individual `http.csp.*` directive above entirely when set. |

See [CORS and CSP](../http/cors-csp.md) for how these compose with a
per-response nonce and a route's own `HttpResponse.csp` override.

## Sessions (JWT)

| Key | Env var | Default | What it does |
|---|---|---|---|
| `jwt.secret` | `REL_JWT__SECRET` | auto-generated | HMAC signing secret — see [Secrets and generated values](index.md#secrets-and-generated-values) above. |
| `jwt.cookie_name` | `REL_JWT__COOKIE_NAME` | `accesstoken` | Name of the cookie carrying the JWT. |
| `jwt.algorithm` | `REL_JWT__ALGORITHM` | `HS256` | `HS256`, `HS384`, or `HS512`. |
| `jwt.same_site` | `REL_JWT__SAME_SITE` | `Lax` | `SameSite` attribute of the JWT cookie: `Strict`, `Lax`, or `None`. |
| `jwt.max_age` | `REL_JWT__MAX_AGE` | `1800` (30 min) | How long a freshly-minted token stays valid. |
| `jwt.renew_after` | `REL_JWT__RENEW_AFTER` | `0.5` | Fraction of a token's own lifespan elapsed before it's renewed on next use. |
| `jwt.max_session_age` | `REL_JWT__MAX_SESSION_AGE` | `604800` (7 days) | Hard ceiling on a session's total lifetime, regardless of renewal. |

Full session lifecycle — minting, renewal, revocation via middleware — is [Authentication](../http/authentication.md).

## SAML (shared SP identity)

| Key | Env var | Default | What it does |
|---|---|---|---|
| `saml.certificate_path` | `REL_SAML__CERTIFICATE_PATH` | generated on first boot | Colon-separated search list for this deployment's SAML SP certificate, shared across every `saml.<name>` entry. |
| `saml.private_key_path` | `REL_SAML__PRIVATE_KEY_PATH` | generated on first boot | Search list for that certificate's private key, same rule as `saml.certificate_path`. |

## OpenID Connect and SAML providers

One named entry per provider — `<name>` is yours to choose, unrelated to the provider's brand.
See [Authentication](../http/authentication.md) for the full picture (routes, callback function
contract, worked examples); this is the exhaustive key list.

| Key pattern | Env var | Default | What it does |
|---|---|---|---|
| `openid.<name>.issuer` | `REL_OPENID__<NAME>__ISSUER` | — (required) | The OIDC issuer URL; `/.well-known/openid-configuration` is discovered from it. |
| `openid.<name>.client_id` / `openid.<name>.client_secret` | `REL_OPENID__<NAME>__CLIENT_ID` / `REL_OPENID__<NAME>__CLIENT_SECRET` | `$FILE$/secrets/openid-<name>.id` / `.secret` | Credentials issued by the IdP when this app was registered with it. |
| `openid.<name>.scopes` | `REL_OPENID__<NAME>__SCOPES` | `openid, email, profile` | Comma-separated OIDC scopes requested. |
| `openid.<name>.fetch_userinfo` | `REL_OPENID__<NAME>__FETCH_USERINFO` | `false` | Also call the userinfo endpoint after token exchange, merging its claims over the ID token's own. |
| `openid.<name>.callback_function` | `REL_OPENID__<NAME>__CALLBACK_FUNCTION` | falls back to `http.functions.sso_callback` | Function invoked with the verified claims. |
| `openid.<name>.public_host` | `REL_OPENID__<NAME>__PUBLIC_HOST` | falls back to `http.public_host` | Overrides the deployment's public host for this provider entry alone. |
| `saml.<name>.idp_metadata_url` | `REL_SAML__<NAME>__IDP_METADATA_URL` | — (required) | Fetched once at startup; retried lazily on the next login attempt if it failed. |
| `saml.<name>.force_signed_requests` | `REL_SAML__<NAME>__FORCE_SIGNED_REQUESTS` | `true` | Sign the outgoing `AuthnRequest` with the SP key. |
| `saml.<name>.callback_function` | `REL_SAML__<NAME>__CALLBACK_FUNCTION` | falls back to `http.functions.sso_callback` | Same as `openid.<name>.callback_function`. |
| `saml.<name>.public_host` | `REL_SAML__<NAME>__PUBLIC_HOST` | falls back to `http.public_host` | Same as `openid.<name>.public_host`. |

## Logging

| Key | Env var | Default | What it does |
|---|---|---|---|
| `logging.handler` | `REL_LOGGING__HANDLER` | `pretty` | `pretty` or `json`. |
| `logging.level` | `REL_LOGGING__LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `logging.filter.<attr>` | `REL_LOGGING__FILTER__<ATTR>` | — | A regexp a log line's `<attr>` must match to be shown at all; a line with nothing to filter against for that key is shown regardless. |
| `logging.exclude.<attr>` | `REL_LOGGING__EXCLUDE__<ATTR>` | — | Same shape, inverted — suppresses a matching line. Applied after any `logging.filter.*` also set. |

## Restricting what a query can reach

Two dynamic namespaces add to (never replace) rel's built-in blacklist:

| Key pattern | Env var | Default | What it does |
|---|---|---|---|
| <code>blacklist.relations.&lt;schema&gt;.&lt;name&#124;*&gt;</code> | <code>REL_BLACKLIST__RELATIONS__&lt;SCHEMA&gt;__&lt;NAME&#124;*&gt;</code> | see below | `y`/`n`; `*` blacklists an entire schema. `pg_catalog.*` and `information_schema.*` are blacklisted by default. |
| <code>blacklist.functions.&lt;schema&gt;.&lt;name&#124;*&gt;</code> | <code>REL_BLACKLIST__FUNCTIONS__&lt;SCHEMA&gt;__&lt;NAME&#124;*&gt;</code> | see below | `y`/`n`; `*` blacklists an entire schema. A handful of individually dangerous functions (`pg_sleep`, `pg_terminate_backend`, the `pg_advisory_*lock*` family, ...) are blacklisted by default. |

```toml
[blacklist.relations.public]
orders = "y"

[blacklist.functions.pg_catalog]
dblink = "y"
```

## Reload

| Key | Env var | Default | What it does |
|---|---|---|---|
| `reload.cmd` | `REL_RELOAD__CMD` | — | Command line rel runs before every reload (startup and every `SIGUSR1`). Empty skips this step entirely. See [Reload](reload.md) for the interpolation syntax. |
| `reload.timeout` | `REL_RELOAD__TIMEOUT` | `120` (seconds) | `reload.cmd` is aborted and considered failed once this elapses. |
| `reload.drain_timeout` | `REL_RELOAD__DRAIN_TIMEOUT` | `30` (seconds) | How long a reload waits for in-flight requests to finish, before `reload.cmd` runs, before cancelling their contexts and proceeding anyway. |

## TypeScript export

| Key | Env var | Default | What it does |
|---|---|---|---|
| `typescript.helper_path` | `REL_TYPESCRIPT__HELPER_PATH` | disabled | Filesystem path rel (re)writes `database.ts`'s content to directly, at startup and on every `SIGUSR1` reload — for an editor/LSP watching a real file on disk. See [TypeScript client](../typescript/index.md). |
