
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
- `auth_time` — when the *session* was first established. Unlike `iat`, this never changes across renewals (see Lifecycle below) — it's what `jwt.maxsessionage` is measured against.
- Anything else is a free-form claim the developer can add.

`iat`, `exp`, and `auth_time` are always computed by rel. If a function's response includes a `jwt` object with any of these three set, the supplied values are ignored and overwritten — a function can control `role` and custom claims, never its own lifetime.

## Configuration

* `jwt.secret` (default `$FILE$jwt-secret$GEN$32`) : the JWT signing secret.
* `jwt.cookiename` (default `accesstoken`) : the cookie scanned and set by rel to carry the JWT.
* `jwt.algorithm` (default `HS256`, one of `HS256` | `HS384` | `HS512`) : the algorithm used to sign the JWT. rel enforces this exact algorithm on verification and rejects any token whose header claims a different one — including `none` — as an invalid signature.
* `jwt.samesite` (default `Lax`) : `SameSite` attribute of the JWT cookie.
* `jwt.maxage` (default `1800`, 30 minutes) : how long a freshly-minted token is valid for, i.e. `exp = iat + jwt.maxage`. A function can override this for its own response via `jwt_attrs.maxage` (see Responses below).
* `jwt.renewafter` (default `0.5`) : fraction of a token's own `exp - iat` after which it's due for renewal (see Lifecycle).
* `jwt.maxsessionage` (default `604800`, 7 days) : hard ceiling on a session's total lifetime, measured from `auth_time`, irrespective of activity. Once exceeded, the session can no longer be renewed and the user must fully re-authenticate.

The JWT cookie's `Max-Age` always mirrors the token's own `exp - iat` — it is never set independently. `http.cookiesmaxage` (see HTTP Configuration below) does not apply to the JWT cookie; it only governs other, non-JWT cookies set via the generic `cookies` field.

## Lifecycle

1. **Mint.** A function establishes a session by setting the `jwt` field on its `RelHttpResponse` (see Responses). This is always treated as a fresh session: `auth_time` and `iat` are set to now, `exp = iat + (jwt_attrs.maxage or jwt.maxage)`, and the cookie's `Max-Age` mirrors that same value.
2. **Verify.** On every subsequent request, rel checks the cookie's signature, `jwt.algorithm`, `exp`, and `auth_time + jwt.maxsessionage`. Failing any of these is equivalent to no session at all — the request proceeds under `jwt.anonrole` (see Roles).
3. **Check.** If the token verifies, and `http.functions.check_session` is configured, rel calls it (see Session invalidation below).
4. **Renew.** If the token verifies and passes the check, and more than `jwt.renewafter` of its own lifespan has elapsed since its `iat`, rel re-mints it: fresh `iat`/`exp` (same `jwt.maxage`/`jwt_attrs.maxage` window as originally minted with), fresh `Set-Cookie`, `auth_time` and `role` unchanged, other claims carried over as-is. Tokens under the `renewafter` threshold pass through unchanged — this bounds `Set-Cookie` churn to roughly once per `maxage × renewafter` of activity rather than once per request.
5. **Apply role.** Only now does rel run `SET LOCAL ROLE "<role>"` (role name escaped as an identifier) for the rest of the request.

A session's total lifetime is therefore bounded twice: `exp` bounds any single token (short, so a leaked/stolen cookie alone is only useful briefly), and `jwt.maxsessionage` bounds how long renewal can keep extending it (so a continuously-replayed valid cookie still forces re-authentication eventually).

## Session invalidation

* `http.functions.check_session` (default empty, disabled) : unquoted, fully qualified name of a Postgres function that lets the database reject a session before `exp`/`jwt.maxsessionage` would otherwise do so — e.g. password change, ban, admin-triggered logout.

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

* `jwt.anonrole` (default `~anonymous`) : the role rel applies when there is no JWT, or the one presented is missing, invalid, expired, session-checked out, or otherwise unusable.

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go; no reason to replace it.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `00-general.md`'s requirement of an easily configurable, generic approach — `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.

Each configured endpoint (SAML IdP, OIDC issuer, ...) is a named entry under its own config namespace (`saml.<name>.*`, `openid.<name>.*`), not detailed further here.

# HTTP

Routing is done with the standard library's `net/http.ServeMux` (Go 1.22+ method/wildcard patterns, e.g. `"GET /api/{schema}/{function}"` with `r.PathValue(...)`) — no external router dependency. This fits rel's actual routing needs: a small, fixed set of patterns (`/api/{schema}/{function}`, `/rel`, `/auth/*`, `/js/*`, static files), since individual database functions are dispatched dynamically from within the `/api/{schema}/{function}` handler rather than registered as their own routes. The JWT lifecycle (Verify/Check/Renew/Apply role, see `# JWT` above) is implemented as ordinary `func(http.Handler) http.Handler` middleware, composed by hand — no framework-specific request/context type involved.

Creating the domain types `RelHttpRequest` and `RelHttpResponse` on JSON or JSONB (their schema doesn't matter) and using them in function prototypes, or creating a domain over `bytea` with a `/` in their name, enables functions to return binary content with the domain name as mime-type.

A function whose name does **not** start with `_`, and that takes 0 arguments or one `RelHttpRequest` argument, and that returns `RelHttpResponse` or a mimetype domain, is a HTTP route function — callable directly at `/api/<schema>/<function_name>` (and, if `http.functions.allowed_routes` is set, only if its fully qualified name also matches that regexp). A leading `_` opts a function out of route discovery unconditionally, with no configuration needed — this is how internal helpers that happen to match the signature stay unexposed.

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

* `http.requestdomainname` (default `RelHttpRequest`) : the fully qualified, unquoted name of the JSON domain for request-typed functions.
* `http.responsedomainname` (default `RelHttpResponse`) : the fully qualified, unquoted name of the JSON domain rel interprets as an HTTP response return type. Not an error if it doesn't exist, but rel will warn, since without it no authentication flow can work.
* `http.templatesdir` (default `/template`) : the template directory.
* `http.cookiesmaxage` (default `86400`) : default max-age for cookies set via the generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT cookie (see `jwt.maxage`).
* `http.functions.auth` (default empty) : regexp restricting which functions' responses rel will honor a `jwt` field from, matched against the fully qualified, unquoted function name. Empty means unrestricted.
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

Unless a response overrides them, rel sets `secure: true`, `httponly: true`, `samesite: Lax`, and a max-age of `http.cookiesmaxage` on any cookie it sets.

The JWT itself is read/set more directly through the `jwt` field on `RelHttpRequest`/`RelHttpResponse`, rather than through `cookies` — see Claims/Lifecycle above. Setting `jwt: null` clears the session (logout).

**Warning:** by default, *any* HTTP route function can set `jwt` on its response and thereby authenticate the caller as any role — the `jsonb` payload is trusted at face value. Set `http.functions.auth` to restrict which functions have this power; consider defaulting it to your login/session functions only (e.g. everything under an `auth` schema), rather than leaving it unrestricted.

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

  cookies: {[name: string]: Cookie}
  jwt: JWT
}
```

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

`jwt_attrs` controls how a `jwt` set by this same response is minted — `maxage` overrides `jwt.maxage` for this session (both the token's `exp - iat` and the cookie's `Max-Age` follow it), `samesite` overrides `jwt.samesite` for this cookie only.

## Postgres Exceptions

Raised while handling an HTTP route function or the session-check function, an exception with code `RSxxx` gives the response status `xxx`, with whatever text was raised as the body.

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Not found' using errcode = 'RS404';
```

Any other error code results in the error page with status 500, unless it's a known Postgres code with unambiguous HTTP semantics (e.g. permission denied → 401).
