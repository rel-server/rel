
# JWT

A JWT is exchanged between the browser and rel in a `secure`, `httponly` cookie.

## Claims

```typescript
type JWT = {
  role: string,
  iat: number,
  exp: number,
  auth_time: number,
} & {[name: string]: unknown}
```

- `role` — the Postgres role the request is mapped to. Required; a JWT without one is an error.
- `iat` — when this particular token was minted.
- `exp` — when this particular token stops being valid.
- `auth_time` — when the *session* was first established. Unlike `iat`, this never changes across renewals (see Lifecycle below) — it's what `jwt.max_session_age` is measured against.
- Anything else is a free-form claim the developer can add.

`iat`, `exp`, and `auth_time` are always computed by rel. If a function's response includes a `jwt` object with any of these three set, the supplied values are ignored and overwritten — a function can control `role` and custom claims, never its own lifetime.

## Configuration

* `jwt.secret` (default `$FILE$jwt-secret$GEN$32`) : the JWT signing secret.
* `jwt.cookie_name` (default `accesstoken`) : the cookie scanned and set by rel to carry the JWT.
* `jwt.algorithm` (default `HS256`, one of `HS256` | `HS384` | `HS512`) : the algorithm used to sign the JWT. rel enforces this exact algorithm on verification and rejects any token whose header claims a different one — including `none` — as an invalid signature.
* `jwt.same_site` (default `Lax`) : `SameSite` attribute of the JWT cookie.
* `jwt.max_age` (default `1800`, 30 minutes) : how long a freshly-minted token is valid for, i.e. `exp = iat + jwt.max_age`. A function can override this for its own response via `jwt_attrs.maxage` (see Responses below).
* `jwt.renew_after` (default `0.5`) : fraction of a token's own `exp - iat` after which it's due for renewal (see Lifecycle).
* `jwt.max_session_age` (default `604800`, 7 days) : hard ceiling on a session's total lifetime, measured from `auth_time`, irrespective of activity. Once exceeded, the session can no longer be renewed and the user must fully re-authenticate.

The JWT cookie's `Max-Age` always mirrors the token's own `exp - iat` — it is never set independently. `http.cookies_max_age` (see HTTP Configuration below) does not apply to the JWT cookie; it only governs other, non-JWT cookies set via the generic `cookies` field.

## Lifecycle

1. **Mint.** A function establishes a session by setting the `jwt` field on its `RelHttpResponse` (see Responses). This is always treated as a fresh session: `auth_time` and `iat` are set to now, `exp = iat + (jwt_attrs.maxage or jwt.max_age)`, and the cookie's `Max-Age` mirrors that same value.
2. **Verify.** On every subsequent request, rel checks the cookie's signature, `jwt.algorithm`, `exp`, and `auth_time + jwt.max_session_age`. Failing any of these is equivalent to no session at all — the request proceeds under `pg.query.anonymous_role` (see Roles).
3. **Check.** If the token verifies, and `http.functions.check_session` is configured, rel calls it (see Session invalidation below).
4. **Renew.** If the token verifies and passes the check, and more than `jwt.renew_after` of its own lifespan has elapsed since its `iat`, rel re-mints it: fresh `iat`/`exp`, using the SAME width the current token's own `exp - iat` already has (so a `jwt_attrs.maxage` override from the original mint keeps applying across renewals without rel needing to remember it separately — the token's own claims are the only state involved), fresh `Set-Cookie`, `auth_time` and `role` unchanged, other claims carried over as-is. The renewed cookie's `SameSite` is always `jwt.same_site` — unlike `maxage`, a `jwt_attrs.samesite` override from the original mint does NOT persist across renewal (nothing about `SameSite` is a JWT claim for rel to carry forward, and adding a private claim just to remember one rarely-used cookie attribute wasn't worth it). Tokens under the `renew_after` threshold pass through unchanged — this bounds `Set-Cookie` churn to roughly once per `maxage × renewafter` of activity rather than once per request.
5. **Apply role.** Only now does rel apply `"<role>"` (role name escaped as an identifier) for the rest of the request — `/rpc` runs `SET LOCAL ROLE`, transaction-scoped, since its whole request shares one transaction start-to-finish ; `/rel` runs session-scoped `SET ROLE`/`RESET ROLE` on its pinned connection instead, since it commits its write transaction and then runs every item's read query AFTER that commit, still on the same connection — see `specs/TODO.md`'s connection-pool/lifecycle entry for the full reasoning.

A session's total lifetime is therefore bounded twice: `exp` bounds any single token (short, so a leaked/stolen cookie alone is only useful briefly), and `jwt.max_session_age` bounds how long renewal can keep extending it (so a continuously-replayed valid cookie still forces re-authentication eventually).

## Session invalidation

* `http.functions.check_session` (default empty, disabled) : unquoted, fully qualified name of a Postgres function that lets the database reject a session before `exp`/`jwt.max_session_age` would otherwise do so — e.g. password change, ban, admin-triggered logout.

Required signature:

```sql
create function auth.check_session(jwt jsonb) returns void
language plpgsql
security definer
as $$
begin
  if <session revoked/disabled> then
    raise exception 'Session revoked' using errcode = 'RS401';
  end if;
end;
$$;
```

- Called once per request that carries a JWT which already passed verification (step 2 above) — never for anonymous requests, since there's nothing to check.
- Runs in the same connection/transaction as the rest of the request's setup, immediately *before* `SET LOCAL ROLE` — it rides the DB round trip rel already makes to apply the role, so it adds no extra round trip. Running before the role switch (and being `security definer`) also lets it consult tables the eventual per-request role has no business reading directly (a revocation list, a `users.disabled` flag).
- Takes the full claims object as `jsonb` — deliberately not the `RelHttpRequest` domain, so it can never be picked up by the HTTP route auto-discovery rule (see HTTP below) regardless of its name.
- Signals rejection the same way HTTP route functions do: `raise exception ... using errcode = 'RSxxx'` aborts the request with that status and clears the JWT cookie, forcing re-authentication. Returning normally means the session stands.
- Rel does not interpret or require any particular claim shape for revocation checks (e.g. a `jti`/session-id claim to look up) — that's an application convention on top of the generic `jsonb` payload.

# Roles

Much like PostgREST, rel sets a role on every request made to Postgres. Authenticating a user generally means assigning them a role in the JWT, which is then applied to every database request the session makes (escaped as an identifier, never interpolated raw).

## Configuration

* `pg.query.anonymous_role` (default `~anonymous`) : the role rel applies when there is no JWT, or the one presented is missing, invalid, expired, session-checked out, or otherwise unusable. Same setting `querying.md ## Configuration` names — defined once, here, since this doc is where its behavior actually lives ; `querying.md`'s own entry should carry this same default rather than leaving it unstated (previously a genuine drift : this doc used to name the identical setting `jwt.anonrole`, a name that never matched `querying.md`'s `pg.query.anonymous_role` at all).

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go; no reason to replace it.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `00-general.md`'s requirement of an easily configurable, generic approach — `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.

Each configured endpoint (SAML IdP, OIDC issuer, ...) is a named entry under its own config namespace (`saml.<name>.*`, `openid.<name>.*`), not detailed further here.

# HTTP

Routing is done with the standard library's `net/http.ServeMux` (Go 1.22+ method/wildcard patterns, e.g. `"GET /rpc/{schema}/{function}"` with `r.PathValue(...)`) — no external router dependency. This fits rel's actual routing needs: a small, fixed set of patterns (`/rpc/{schema}/{function}`, `/rel`, `/auth/*`, `/js/*`, static files), since individual database functions are dispatched dynamically from within the `/rpc/{schema}/{function}` handler rather than registered as their own routes. Verify and Renew (steps 2 and 4 — see `# JWT` above) need no database and are implemented as ordinary `func(http.Handler) http.Handler` middleware, composed by hand — no framework-specific request/context type involved. Check and Apply role (steps 3 and 5) need the request's own DB connection, which doesn't exist yet when generic middleware runs, so those two are each handler's own responsibility instead (`/rel`, `/rpc`) once a connection is acquired — see `specs/TODO.md`'s connection-pool/lifecycle entry.

> Why `/rpc`, not `/api` : `/api` describes nothing about what the route actually does (every HTTP endpoint in existence is "an API"). `/rpc` names the actual mechanism — a named backend function called directly over HTTP — and matches PostgREST's own convention for the identical concept, which `# Roles` below already invokes as a point of comparison.

Creating the domain types `RelHttpRequest` and `RelHttpResponse` on JSON or JSONB (their schema doesn't matter) and using them in function prototypes, or creating a domain over `bytea` with a `/` in their name, enables functions to return binary content with the domain name as mime-type.

A function whose name does **not** start with `_`, and that takes 0 arguments or one `RelHttpRequest` argument, and that returns `RelHttpResponse` or a mimetype domain, is a HTTP route function — callable directly at `/rpc/<schema>/<function_name>` (and, if `http.functions.allowed_routes` is set, only if its fully qualified name also matches that regexp). A leading `_` opts a function out of route discovery unconditionally, with no configuration needed — this is how internal helpers that happen to match the signature stay unexposed.

Such a function's name can end with `__VERB` (`__GET`, `__POST`, ...) to restrict which HTTP verb it answers to; case doesn't matter. If a verb-suffixed function is defined alongside an unsuffixed one, the unsuffixed function is the fallback for verbs with no specific match. Conforming to web semantics (e.g. `__GET` must not mutate state — see the CSRF note under Cookies) is the function author's responsibility; rel does not enforce it.

`RelHttpResponse` can render via [Jet templates](github.com/CloudyKit/jet) through the optional `template` key.

```sql
create domain "RelHttpRequest" as jsonb;
create domain "RelHttpResponse" as jsonb;
create domain "image/png" AS bytea;
create function schema.some_function(req RelHttpRequest) returns RelHttpResponse /* ... */;
create function schema.returns_binary() returns "image/png" /* ... */;
```

## Configuration

* `http.request_domain_name` (default `RelHttpRequest`) : the fully qualified, unquoted name of the JSON domain for request-typed functions.
* `http.response_domain_name` (default `RelHttpResponse`) : the fully qualified, unquoted name of the JSON domain rel interprets as an HTTP response return type. Not an error if it doesn't exist, but rel will warn, since without it no authentication flow can work.
* `http.templatesdir` (default `/template`) : the template directory.
* `http.cookies_max_age` (default `86400`) : default max-age for cookies set via the generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT cookie (see `jwt.max_age`).
* `http.functions.allowed_auth` (default empty) : regexp restricting which functions' responses rel will honor a `jwt` field from, matched against the fully qualified, unquoted function name. Empty means unrestricted.
* `http.functions.allowed_routes` (default empty) : regexp a function's fully qualified, unquoted name must additionally match to become a public route, on top of having the right signature and not starting with `_`. Empty means unrestricted (any matching-signature, non-`_` function is routed).

## Cookies

The `cookies` field, present on both `RelHttpRequest` and `RelHttpResponse`, is a convenience over manipulating `Set-Cookie`/`Cookie` headers directly.

```typescript
interface Cookie {
  value: string
  httponly: boolean
  secure: boolean
  samesite: string
  maxage: number
}
```

Unless a response overrides them, rel sets `secure: true`, `httponly: true`, `samesite: Lax`, and a max-age of `http.cookies_max_age` on any cookie it sets.

The JWT itself is read/set more directly through the `jwt` field on `RelHttpRequest`/`RelHttpResponse`, rather than through `cookies` — see Claims/Lifecycle above. Setting `jwt: null` clears the session (logout).

**Warning:** by default, *any* HTTP route function can set `jwt` on its response and thereby authenticate the caller as any role — the `jsonb` payload is trusted at face value. Set `http.functions.allowed_auth` to restrict which functions have this power; consider defaulting it to your login/session functions only (e.g. everything under an `auth` schema), rather than leaving it unrestricted.

`SameSite=Lax` on both the JWT cookie and default-configured cookies, combined with the requirement that `__GET` functions not mutate state, is rel's CSRF defense — there is no separate CSRF token mechanism.

## Request

If a function has exactly one argument of type `RelHttpRequest`, the request is encoded as:

```typescript
interface RelHttpRequest {
  method: string
  uri: string
  headers: {[name: string]: string[]}
  content_type: string
  content: string // may be ""

  cookies: {[name: string]: string} // value only — see the note below
  jwt: JWT | null // null when the request carries no valid session
}
```

`RelHttpRequest.cookies` deliberately does NOT reuse the full `Cookie` shape (`value`/`httponly`/`secure`/`samesite`/`maxage`) the response side uses — a browser's `Cookie` header only ever sends `name=value`, the other four attributes are response-only (`Set-Cookie` attributes) and can never be known for an inbound cookie. Value-only avoids four fields that would always be empty/false/zero.

## Responses

If a function's return type is `RelHttpResponse`, rel expects the following shape and handles it as a response:

```typescript
interface RelHttpResponse {
  status: number // 200 if not specified
  content_type: string
  content: unknown

  template?: string
  headers?: {[name: string]: string | string[]}
  cookies?: {[name: string]: Cookie | string}
  jwt?: JWT | null
  jwt_attrs?: {
    samesite?: string
    maxage?: number
  }
}
```

`jwt_attrs` controls how a `jwt` set by this same response is minted — `maxage` overrides `jwt.max_age` for this session (both the token's `exp - iat` and the cookie's `Max-Age` follow it), `samesite` overrides `jwt.same_site` for this cookie only.

## Postgres Exceptions

Raised while handling an HTTP route function or the session-check function, an exception with code `RSxxx` gives the response status `xxx`, with whatever text was raised as the body.

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Not found' using errcode = 'RS404';
```

Any other error code results in the error page with status 500, unless it's a known Postgres code with unambiguous HTTP semantics (e.g. permission denied → 401).
