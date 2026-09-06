---
icon: material/format-list-bulleted
---

# HTTP reference

Every other page in this section teaches one piece of the HTTP layer by example. This page is
the other kind of reference: every domain name, every function signature rel recognizes, and
every `http.*`/`jwt.*`/SSO configuration key, in one place, each linked to the page that
explains it. Skim this first to see the whole surface at a glance, or come back to it once you
know roughly what you're looking for and just need the exact name or default.

## Domain names

Four names rel looks for by matching a Postgres domain's bare name — each one's own resolution
rule (unqualified search across every schema, more than one match across schemas is a fatal
startup error, zero matches just disables whatever needs it) is covered on its own page, linked
below.

| Domain | Config key | Default | What it's for |
|---|---|---|---|
| `RelHttpRequest` | `http.request_domain_name` | `RelHttpRequest` | The shape a route function's `req` argument receives. See [Requests and responses ## `RelHttpRequest`](requests-responses.md#relhttprequest). |
| `RelHttpResponse` | `http.response_domain_name` | `RelHttpResponse` | The shape a route function — or an SSO callback function — must return. See [Requests and responses ## `RelHttpResponse`](requests-responses.md#relhttpresponse). |
| `RelUpload` | `http.upload_domain_name` | `RelUpload` | Threaded between an upload-destination pair's `__prepare` and mandatory function. See [File uploads ## Choosing a destination without routing bytes through Postgres](uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |
| a mimetype domain, e.g. `"image/png"` | — not configurable; matched by name pattern (contains `/`) and underlying type (`bytea`/`text`) | — | Lets a route return raw binary/text content directly, instead of JSON. See [Static files ## Returning binary or text content directly](static-files.md#returning-binary-or-text-content-directly). |

## Function prototypes

Every distinct callable-function shape the HTTP layer recognizes. The first five are *routes* —
discovered automatically at `/route/<schema>/<function>`, subject to the `__VERB` suffix
convention and `http.functions.allowed_routes` (see [HTTP routes ## What makes a function a
route](index.md#what-makes-a-function-a-route)). The rest are never routes — nothing calls them
over `/route` directly, or discovers them by scanning the schema; rel invokes each one itself,
by the exact name configured for its own purpose.

### Routes

| Signature | Returns | What it does |
|---|---|---|
| `fn()` | `RelHttpResponse` or a mimetype domain | A route needing nothing from the request itself. |
| `fn(req RelHttpRequest)` | `RelHttpResponse` or a mimetype domain | The common case — read `req.body`/`req.query`/`req.jwt`/etc., return a response. See [HTTP routes](index.md) and [Requests and responses](requests-responses.md). |
| `fn(req RelHttpRequest, files bytea[])` | `RelHttpResponse` or a mimetype domain | Receive one or more uploaded parts' raw bytes. See [File uploads ## Receiving raw bytes](uploads.md#receiving-raw-bytes). |
| `fn(req RelHttpRequest, files bytea[], parts_headers jsonb)` | `RelHttpResponse` or a mimetype domain | Same, plus each part's own `name`/`filename`/`content_type`/`headers`. See [File uploads ## Receiving raw bytes](uploads.md#receiving-raw-bytes). |
| `<name>__prepare(req RelHttpRequest, part jsonb)` and, alongside it, `<name>(req RelHttpRequest, upload RelUpload)` | `RelUpload` (prepare) / `RelHttpResponse` (mandatory) | A pair, both required, streaming an upload straight to disk without its bytes passing through Postgres. Never composes with a `__VERB` suffix. See [File uploads ## Choosing a destination without routing bytes through Postgres](uploads.md#choosing-a-destination-without-routing-bytes-through-postgres). |

### Invoked directly by rel, never discovered as routes

| Signature | Configured via | What it does |
|---|---|---|
| `fn(jwt jsonb) returns void` | `http.functions.check_session` | Called once per authenticated request, before the role switch; raise to revoke the session early. See [Authentication ## Session lifecycle](authentication.md#session-lifecycle). |
| `fn(payload jsonb) returns RelHttpResponse` | `http.functions.sso_callback`, or per-provider `openid.<name>.callback_function`/`saml.<name>.callback_function` | Turns a verified OIDC/SAML identity assertion into a role, the same way a login route mints a session. `payload` is `{jwt, identity, state}`. See [Authentication ## OpenID Connect and SAML](authentication.md#openid-connect-and-saml). |
| `fn(payload jsonb) returns void` | `http.static.access.<name>.function` | Gates a subpath of `/static/*`; raise to reject. `payload` is `{path, jwt}`. See [Static files ## Restricting access to part of the tree](static-files.md#restricting-access-to-part-of-the-tree). |

## Configuration

Every `http.*`/`jwt.*` key, plus the handful of `pg.*`/`openid.*`/`saml.*` keys that are really
part of the same HTTP-facing picture. [Configuration](../configuration/index.md) is the
authoritative source for how any of these are actually supplied (env var, flag, or config
file) and for every key rel understands, HTTP or not — this table is the HTTP-scoped subset,
each row pointing at the page that explains the *behavior*, not just the key.

### Server and domains

| Key | Default | Purpose |
|---|---|---|
| `http.host` / `http.port` | all interfaces / `8080` | Listen address. |
| `http.public_host` | unset | This deployment's externally-reachable host (bare, no scheme) — required for OIDC/SAML redirect URLs. See [Authentication](authentication.md). |
| `http.request_domain_name` / `http.response_domain_name` / `http.upload_domain_name` | `RelHttpRequest` / `RelHttpResponse` / `RelUpload` | Domain names — see [Domain names](#domain-names) above. |
| `http.cookies_max_age` | `86400` (seconds) | Default max-age for a cookie set via the generic `cookies` field. Never applies to the JWT cookie. See [Requests and responses ## `RelHttpResponse`](requests-responses.md#relhttpresponse). |
| `http.max_body_size` | 10 MiB | Hard cap on a `/route` request's entire body. See [File uploads](uploads.md#receiving-raw-bytes). |
| `http.max_part_count` | `100` | Max `multipart/form-data` parts per request. See [File uploads](uploads.md#receiving-raw-bytes). |
| `http.static.path` | `/static` | Colon-separated directories served at `/static/*`. See [Static files](static-files.md). |
| `http.templates.path` | `/template` | Directory Jet templates load from. See [Rendering HTML with templates](templates.md). |

### Route function gating

| Key | Default | Purpose |
|---|---|---|
| `http.functions.allowed_routes` | unrestricted | Regexp restricting which functions are discovered as routes at all. See [HTTP routes](index.md#what-makes-a-function-a-route). |
| `http.functions.allowed_auth` | unrestricted | Regexp restricting which route functions may set `jwt` on their response. See [Authentication](authentication.md#minting-a-session). |
| `http.functions.check_session` | disabled | See [Function prototypes](#function-prototypes) above and [Authentication](authentication.md#session-lifecycle). |
| `http.functions.sso_callback` | disabled | See [Function prototypes](#function-prototypes) above and [Authentication](authentication.md#openid-connect-and-saml). |

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
| `openid.<name>.callback_function` | falls back to `http.functions.sso_callback` | See [Function prototypes](#function-prototypes) above. |
| `openid.<name>.public_host` | falls back to `http.public_host` | Per-entry override, for a deployment reachable at more than one domain. |
| `saml.<name>.idp_metadata_url` | — (required) | Fetched once at startup; retried lazily on the next login attempt if it failed. |
| `saml.<name>.force_signed_requests` | `true` | Sign the outgoing `AuthnRequest` with the SP key. |
| `saml.<name>.callback_function` | falls back to `http.functions.sso_callback` | Same as `openid.<name>.callback_function`. |
| `saml.<name>.public_host` | falls back to `http.public_host` | Same as `openid.<name>.public_host`. |
| `saml.certificate_path` / `saml.private_key_path` | generated on first boot | This deployment's SP certificate/key, shared across every `saml.<name>` entry. |

### Static file access control

| Key | Default | Purpose |
|---|---|---|
| `http.static.access.<name>.prefix` | — (required) | Subpath, under `/static/`, this rule gates. See [Static files ## Restricting access to part of the tree](static-files.md#restricting-access-to-part-of-the-tree). |
| `http.static.access.<name>.function` | — (required) | See [Function prototypes](#function-prototypes) above. |
