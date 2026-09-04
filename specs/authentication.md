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

The JWT cookie's `Max-Age` always mirrors the token's own `exp - iat` — it is never set independently. `http.cookies_max_age` (see `route.md ## Configuration`) does not apply to the JWT cookie; it only governs other, non-JWT cookies set via the generic `cookies` field.

## Lifecycle

1. **Mint.** A function establishes a session by setting the `jwt` field on its `RelHttpResponse` (see `route.md ## Responses`). This is always treated as a fresh session: `auth_time` and `iat` are set to now, `exp = iat + (jwt_attrs.maxage or jwt.max_age)`, and the cookie's `Max-Age` mirrors that same value.
2. **Verify.** On every subsequent request, rel checks the cookie's signature, `jwt.algorithm`, `exp`, and `auth_time + jwt.max_session_age`. Failing any of these is equivalent to no session at all — the request proceeds under `pg.query.anonymous_role` (see Roles).
3. **Check.** If the token verifies, and `http.functions.check_session` is configured, rel calls it (see Session invalidation below).
4. **Renew.** If the token verifies and passes the check, and more than `jwt.renew_after` of its own lifespan has elapsed since its `iat`, rel re-mints it: fresh `iat`/`exp`, using the SAME width the current token's own `exp - iat` already has, fresh `Set-Cookie`, `auth_time` and `role` unchanged, other claims carried over as-is. The renewed cookie's `SameSite` is always `jwt.same_site` — a `jwt_attrs.samesite` override from the original mint does NOT persist across renewal. Tokens under the `renew_after` threshold pass through unchanged.
   > Why: reusing the current token's own width means a `jwt_attrs.maxage` override from the original mint keeps applying across renewals without rel needing to remember it separately — the token's own claims are the only state involved. `SameSite` isn't reapplied because it isn't a JWT claim rel could carry forward without adding a private claim for one rarely-used cookie attribute. Passing unchanged tokens through bounds `Set-Cookie` churn to roughly once per `maxage × renewafter` of activity rather than once per request.
5. **Apply role.** Only now does rel apply `"<role>"` (role name escaped as an identifier) for the rest of the request — `/route` runs `SET LOCAL ROLE`, transaction-scoped, since its whole request shares one transaction start-to-finish ; `/rel` runs session-scoped `SET ROLE`/`RESET ROLE` on its pinned connection instead, since it commits its write transaction and then runs every item's read query AFTER that commit, still on the same connection (`specs/TODO.md`'s connection-pool/lifecycle entry).

A session's total lifetime is bounded twice: `exp` bounds any single token (short, so a leaked/stolen cookie alone is only useful briefly), and `jwt.max_session_age` bounds how long renewal can keep extending it (so a continuously-replayed valid cookie still forces re-authentication eventually).

**Request handling order, and what it means for `RelHttpRequest.jwt`.** On `/route`, Verify (step 2) runs first, needing no DB connection. Then the anonymous-access/route-authorization checks (`## Anonymous role existence` below, `route.md ## Anonymous route authorization`) run, then the request body is fully read and `RelHttpRequest` is built — all BEFORE a pool connection is acquired. Only then does the connection get acquired, `## Session invalidation`'s Check run, Renew (step 4) happen, and Apply role (step 5) apply.

> Why: this ordering means a request already known to be unauthorized, or malformed/oversized, never holds a pool connection open and never costs a connection-acquire round trip. `/rel` already reads and resolves its own request body before acquiring a connection for the same reason.

`RelHttpRequest.jwt`, embedded into the request BEFORE Renew runs, always reflects the token AS VERIFIED — the claims actually presented for this request — never a renewal this same request happens to trigger. Renewal only ever changes `iat`/`exp` (never `role` or any custom claim), so a route function reading `req.jwt.role` or its own custom claims sees the same values either way ; only `req.jwt.iat`/`req.jwt.exp` could in principle differ from what ends up on the (separately, later) renewed cookie sent back to the client.

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

- Called once per request that carries a JWT which already passed verification (step 2 above) — never for anonymous requests.
- Runs in the same connection/transaction as the rest of the request's setup, immediately *before* `SET LOCAL ROLE` — no extra round trip. Running before the role switch, and being `security definer`, lets it consult tables the eventual per-request role has no business reading directly (a revocation list, a `users.disabled` flag).
- Takes the full claims object as `jsonb`, not the `RelHttpRequest` domain, so it is never picked up by `route.md`'s own route auto-discovery rule regardless of its name.
- Signals rejection the same way HTTP route functions do: `raise exception ... using errcode = 'RSxxx'` aborts the request with that status and clears the JWT cookie, forcing re-authentication. Returning normally means the session stands.
- Rel does not interpret or require any particular claim shape for revocation checks (e.g. a `jti`/session-id claim to look up) — that's an application convention on top of the generic `jsonb` payload.

# Roles

Much like PostgREST, rel sets a role on every request made to Postgres. Authenticating a user generally means assigning them a role in the JWT, which is then applied to every database request the session makes (escaped as an identifier, never interpolated raw).

Anonymous access is opt-in, not ambient : it's either anonymous, or it's verboten — there is no third, implicit "public" state. See `## Anonymous role existence` below for what governs this.

## Configuration

* `pg.query.anonymous_role` (default `~anonymous`) : the role rel applies when there is no JWT, or the one presented is missing, invalid, expired, session-checked out, or otherwise unusable. The identical setting `query-engine.md ## Configuration` names ; this document is the authoritative source for its behavior.

## Anonymous role existence

`pg.query.anonymous_role`'s default (`~anonymous`) is a naming convention, not itself a security posture. What governs whether anonymous access exists at all is whether a role by that name — or whatever the setting is explicitly configured to — is FOUND TO EXIST in the database, checked at introspection time (startup, and any future schema reload — see `specs/TODO.md`'s reload entry).

This is a database-existence check, not a config-emptiness check.

> Why: an operator who sets `pg.query.anonymous_role` to a custom name but forgets to `CREATE ROLE` it gets the same protection, and the same diagnostic, as one who leaves the setting at its default and never creates `~anonymous` at all. A config-emptiness check would miss that first case, letting the request sail through to `SET ROLE` at request time and fail with a confusing runtime error instead of a clear one at startup.

* **Found to exist** : anonymous access is enabled, unchanged from every other behavior this document already describes.
* **Not found** : rel logs a warning at startup/reload (`configured anonymous role %q does not exist — all anonymous requests will be denied`) and proceeds with anonymous access disabled. NOT a fatal error — "no anonymous access at all" is a common, valid configuration. Same non-fatal-but-warn treatment `route.md`'s own route discovery gives `http.response_domain_name` not resolving to anything.

With anonymous access disabled, every unauthenticated request — to `/rel` AND `/route` alike — is rejected with `401`, immediately : before `/route`'s route lookup, before any request body is read, before a pool connection is ever acquired.

> Why: `SET ROLE ""`/`SET LOCAL ROLE ""` is a Postgres syntax error, and skipping the role switch entirely would silently run the request as whatever role the pool connection already has — a privilege escalation for anonymous callers.

## Deployment prerequisite : role membership

`SET ROLE`/`SET LOCAL ROLE` only succeeds when the connecting role (`pg.query.user`, or `pg.user` when `pg.query.user` is unset) is a MEMBER of the role being switched to. This is ordinary Postgres privilege behavior, not something rel enforces or checks — it is the deploying developer's own responsibility to satisfy, and getting it wrong fails at request time, not at startup.

Grant membership in `pg.query.anonymous_role`, and in every role any JWT in the deployment may carry, to the connecting role :

```sql
grant "~anonymous" to query_user;
grant "editor" to query_user;
grant "admin" to query_user;
-- one grant per role the connecting role must be able to switch into
```

Skipping a grant doesn't fail at startup — introspection and dmut migrations both run under the PRIMARY connection (`pg.user`, never `pg.query.user`), so a missing grant is invisible until the first real request tries to `SET ROLE` into the ungranted role, at which point it `500`s with "permission denied to set role". A local/testcontainer deployment connecting as a superuser never observes this at all — superusers can `SET ROLE` to anything.

PostgREST documents the identical prerequisite for its own `authenticator`/`web_anon` pattern.

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`.
  > Why: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `index.md`'s requirement of an easily configurable, generic approach. `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.

Each configured endpoint (SAML IdP, OIDC issuer, ...) is a named entry under its own config namespace (`saml.<name>.*`, `openid.<name>.*`), not detailed further here.
