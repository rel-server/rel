---
icon: material/notebook
---

# HTTP reference

Every other page in this section teaches one piece of the HTTP layer by example. This page is
the other kind of reference: every function signature rel recognizes, and every
`http.*`/`route.*`/`jwt.*`/SSO configuration key, in one place, each linked to the page that
explains it. Skim this first to see the whole surface at a glance, or come back to it once you
know roughly what you're looking for and just need the exact name or default.

## Function prototypes

Every distinct callable-function shape the HTTP layer recognizes, per [HTTP routes ## Function
prototype](index.md#function-prototype).

### Declared routes and middleware

Discovered from a `route::`/`route:{...}` comment or a `route.<schema>.<function>.*` config
entry — never by scanning the schema automatically.

| Signature | Returns | What it does |
|---|---|---|
| `fn()` | a plain type, per the mimetype table below | A route needing nothing from the request itself. |
| `fn(req jsonb)` | a plain type | The common case — read `req.body`/`req.query`/`req.jwt`/etc., return a value directly. See [Requests and responses](requests-responses.md). |
| `fn(req jsonb, files bytea[])` | a plain type | Receive one or more multipart parts' raw bytes. See [File uploads ## Receiving raw bytes](uploads.md#receiving-raw-bytes). |
| `fn(req jsonb, file bytea)` | a plain type | Receive a single raw (non-multipart) request body's bytes. See [File uploads ## Receiving raw bytes](uploads.md#receiving-raw-bytes). |
| `fn(req jsonb, ..., out resp jsonb, out content <type>) returns record` | `resp`'s own fields, `content` per the mimetype table | Full control over the response — status, cookies, `jwt`, `template`, `static_file`. Mandatory for `middleware`/`stream_upload`. See [Requests and responses](requests-responses.md). |
| `fn(req jsonb, out resp jsonb, out content jsonb) returns record`, declared `stream_upload: true` | as above | Streams a single upload straight to disk, called twice. Must not take a `bytea`/`bytea[]` argument. See [File uploads ## Choosing a destination without routing bytes through Postgres](uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| Any of the above, declared `middleware: true` | full-control shape only | Runs ahead of every route/`/rel`/static file under its own path prefix. See [HTTP routes ## Middleware](index.md#middleware). |

A function may also declare extra `text`-typed arguments, named after `{placeholder}`
segments in its own declared path — see [HTTP routes ## Function
prototype](index.md#function-prototype).

**Single-return mimetype table** — applies to a plain return type, or a full-control
function's second `OUT` column when its own response doesn't override `content_type`:

| Return type | `Content-Type` |
|---|---|
| `text` | `text/plain` |
| `json`/`jsonb` | `application/json` |
| `bytea` | `application/octet-stream` |
| a domain whose name contains `/` (over `bytea` or `text`) | the domain's own name, e.g. `"image/png"` |

### Invoked directly by rel, never discovered as routes

| Signature | Configured via | What it does |
|---|---|---|
| `fn(payload jsonb) returns jsonb` (full-control shape) | `http.functions.sso_callback`, or per-provider `openid.<name>.callback_function`/`saml.<name>.callback_function` | Turns a verified OIDC/SAML identity assertion into a role, the same way a login route mints a session. `payload` is `{jwt, identity, state}`. See [Authentication ## OpenID Connect and SAML](authentication.md#openid-connect-and-saml). |

## Configuration

Every `http.*`/`route.*`/`jwt.*` key, plus the handful of `pg.*`/`openid.*`/`saml.*` keys that
are really part of the same HTTP-facing picture. [Configuration reference](../configuration/reference.md)
is the authoritative source for every key rel understands, HTTP or not — this table is the
HTTP-scoped subset, each row pointing at the page that explains the *behavior*, not just the key.

### Server and static files

| Key | Default | Purpose |
|---|---|---|
| `http.host` / `http.port` | all interfaces / `8080` | Listen address. |
| `http.public_host` | unset | This deployment's externally-reachable host (bare, no scheme) — required for OIDC/SAML redirect URLs. See [Authentication](authentication.md). |
| `http.cookies_max_age` | `86400` (seconds) | Default max-age for a cookie set via the generic `cookies` field. Never applies to the JWT cookie. See [Requests and responses](requests-responses.md#httpresponse). |
| `http.max_body_size` | 10 MiB | Hard cap on a declared route request's entire body. See [File uploads](uploads.md#receiving-raw-bytes). |
| `http.max_upload_size` | = `http.max_body_size` | Hard cap on a `stream_upload` route's streamed payload — independent of `http.max_body_size` since this path streams to disk, not memory. See [File uploads](uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `http.max_part_count` | `100` | Max `multipart/form-data` parts per request. See [File uploads](uploads.md#receiving-raw-bytes). |
| `http.static.path` | `/static` | Colon-separated filesystem directories served at the router root, as the fallback for any path no declared route claims. See [Static files](static-files.md). |
| `http.templates.path` | `/template` | Directory Jet templates load from. See [Rendering HTML with templates](templates.md). |

### Route declaration and gating

| Key | Default | Purpose |
|---|---|---|
| `route.<schema>.<function>.path` | — (required to make the function routable) | The chi-syntax path. See [HTTP routes ## Declaring a route](index.md#declaring-a-route). |
| `route.<schema>.<function>.method` | inferred | Comma-separated accepted methods. See [HTTP routes ## Method inference](index.md#function-prototype). |
| `route.<schema>.<function>.template` | unset | Default Jet template, used when the response doesn't set its own. See [Templates](templates.md). |
| `route.<schema>.<function>.stream_upload` | `false` | Flags the two-call disk-streaming upload flow. See [File uploads](uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| `route.<schema>.<function>.middleware` | `false` | Flags a middleware function. See [HTTP routes ## Middleware](index.md#middleware). |
| `http.functions.allowed_auth` | unrestricted | Regexp restricting which route/middleware functions may set `jwt` on their response. See [Authentication](authentication.md#minting-a-session). |
| `http.functions.sso_callback` | disabled | See [Function prototypes](#invoked-directly-by-rel-never-discovered-as-routes) above and [Authentication](authentication.md#openid-connect-and-saml). |

A route is declared either way — a `route::`/`route:{...}` comment on the function, or the
config keys above; config overrides a comment declaration on the same function, with a
warning. See [HTTP routes ## Declaring a route](index.md#declaring-a-route).

### CORS

| Key | Default | Purpose |
|---|---|---|
| `http.cors.allowed_origins` | empty (closed) | Exact origins allowed cross-origin, comma-separated, or `*`. See [CORS and CSP](cors-csp.md#cors). |
| `http.cors.allowed_methods` | `GET, POST, PUT, PATCH, DELETE, OPTIONS` | Methods a preflight may approve. |
| `http.cors.allowed_headers` | `Content-Type` | Extra request headers a preflight may approve. |
| `http.cors.max_age` | `600` (seconds) | How long a browser may cache one preflight response. |

### CSP

| Key | Default | Purpose |
|---|---|---|
| `http.csp.default_src` | `'self'` | Baseline directive, applied unless a more specific one below overrides it. See [CORS and CSP](cors-csp.md#csp). |
| `http.csp.script_src` / `style_src` / `img_src` / `font_src` / `connect_src` / `object_src` / `frame_ancestors` / `base_uri` / `form_action` | falls back to `default_src` | Overrides that one directive specifically. |
| `http.csp.policy` | unset | Full, raw header value — replaces every individual `http.csp.*` directive when set. |

### Sessions (JWT)

| Key | Default | Purpose |
|---|---|---|
| `jwt.secret` | generated into `/secrets/jwt-secret` | HMAC signing secret. See [Authentication](authentication.md). |
| `jwt.cookie_name` | `accesstoken` | Name of the cookie carrying the JWT. |
| `jwt.algorithm` | `HS256` | `HS256`, `HS384`, or `HS512`. |
| `jwt.same_site` | `Lax` | `SameSite` attribute of the JWT cookie. |
| `jwt.max_age` | `1800` (30 min) | Freshly-minted token lifetime. |
| `jwt.renew_after` | `0.5` | Fraction of a token's lifespan elapsed before it's renewed on next use. |
| `jwt.max_session_age` | `604800` (7 days) | Hard ceiling on a session's total lifetime, regardless of renewal. |
| `pg.query.anonymous_role` | `~anonymous` | The role a request with no valid session runs as. See [Authentication](authentication.md). |

### OpenID Connect and SAML

| Key | Default | Purpose |
|---|---|---|
| `openid.<name>.issuer` | — (required) | OIDC issuer URL; discovery is fetched from it. See [Authentication ## OpenID Connect and SAML](authentication.md#openid-connect-and-saml). |
| `openid.<name>.client_id` / `client_secret` | read from `/secrets/openid-<name>.id`/`.secret` | Credentials issued by the IdP. |
| `openid.<name>.scopes` | `openid, email, profile` | Requested OIDC scopes. |
| `openid.<name>.fetch_userinfo` | `false` | Also call the userinfo endpoint, merging its claims over the ID token's own. |
| `openid.<name>.callback_function` | falls back to `http.functions.sso_callback` | See [Function prototypes](#invoked-directly-by-rel-never-discovered-as-routes) above. |
| `openid.<name>.public_host` | falls back to `http.public_host` | Per-entry override, for a deployment reachable at more than one domain. |
| `saml.<name>.idp_metadata_url` | — (required) | Fetched once at startup; retried lazily on the next login attempt if it failed. |
| `saml.<name>.force_signed_requests` | `true` | Sign the outgoing `AuthnRequest` with the SP key. |
| `saml.<name>.callback_function` | falls back to `http.functions.sso_callback` | Same as `openid.<name>.callback_function`. |
| `saml.<name>.public_host` | falls back to `http.public_host` | Same as `openid.<name>.public_host`. |
| `saml.certificate_path` / `saml.private_key_path` | generated on first boot | This deployment's SP certificate/key, shared across every `saml.<name>` entry. |
| `saml.<name>.certificate_path` / `saml.<name>.private_key_path` | falls back to `saml.certificate_path` / `saml.private_key_path` | Per-entry override — same generate-if-missing behavior, scoped to this entry alone. |
