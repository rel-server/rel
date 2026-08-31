
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

The JWT cookie's `Max-Age` always mirrors the token's own `exp - iat` — it is never set independently. `http.cookies_max_age` (see `rpc.md ## Configuration`) does not apply to the JWT cookie; it only governs other, non-JWT cookies set via the generic `cookies` field.

## Lifecycle

1. **Mint.** A function establishes a session by setting the `jwt` field on its `RelHttpResponse` (see `rpc.md ## Responses`). This is always treated as a fresh session: `auth_time` and `iat` are set to now, `exp = iat + (jwt_attrs.maxage or jwt.max_age)`, and the cookie's `Max-Age` mirrors that same value.
2. **Verify.** On every subsequent request, rel checks the cookie's signature, `jwt.algorithm`, `exp`, and `auth_time + jwt.max_session_age`. Failing any of these is equivalent to no session at all — the request proceeds under `pg.query.anonymous_role` (see Roles).
3. **Check.** If the token verifies, and `http.functions.check_session` is configured, rel calls it (see Session invalidation below).
4. **Renew.** If the token verifies and passes the check, and more than `jwt.renew_after` of its own lifespan has elapsed since its `iat`, rel re-mints it: fresh `iat`/`exp`, using the SAME width the current token's own `exp - iat` already has (so a `jwt_attrs.maxage` override from the original mint keeps applying across renewals without rel needing to remember it separately — the token's own claims are the only state involved), fresh `Set-Cookie`, `auth_time` and `role` unchanged, other claims carried over as-is. The renewed cookie's `SameSite` is always `jwt.same_site` — unlike `maxage`, a `jwt_attrs.samesite` override from the original mint does NOT persist across renewal (nothing about `SameSite` is a JWT claim for rel to carry forward, and adding a private claim just to remember one rarely-used cookie attribute wasn't worth it). Tokens under the `renew_after` threshold pass through unchanged — this bounds `Set-Cookie` churn to roughly once per `maxage × renewafter` of activity rather than once per request.
5. **Apply role.** Only now does rel apply `"<role>"` (role name escaped as an identifier) for the rest of the request — `/rpc` runs `SET LOCAL ROLE`, transaction-scoped, since its whole request shares one transaction start-to-finish ; `/rel` runs session-scoped `SET ROLE`/`RESET ROLE` on its pinned connection instead, since it commits its write transaction and then runs every item's read query AFTER that commit, still on the same connection — see `specs/TODO.md`'s connection-pool/lifecycle entry for the full reasoning.

A session's total lifetime is therefore bounded twice: `exp` bounds any single token (short, so a leaked/stolen cookie alone is only useful briefly), and `jwt.max_session_age` bounds how long renewal can keep extending it (so a continuously-replayed valid cookie still forces re-authentication eventually).

**Request handling order, and what it means for `RelHttpRequest.jwt`.** On `/rpc`, Verify (step 2 above) runs first — it's what makes "is this request anonymous" knowable at all, and needs no DB connection. Then the anonymous-access/route-authorization checks (`## Anonymous role existence` below, `rpc.md ## Anonymous route authorization`) run, then the request body is fully read and `RelHttpRequest` is built — all of this BEFORE a pool connection is acquired. Only then does the connection get acquired, `## Session invalidation`'s Check run, Renew (step 4 above) happen, and Apply role (step 5) apply — deliberately, so a request already known to be unauthorized, or that turns out to be malformed/oversized, never holds a pool connection open at all, and never costs a connection-acquire round trip in the first place. `/rel` already reads and resolves its own request body before acquiring a connection for exactly the same reason ; `/rpc`'s ordering here brings it in line with that, not a new pattern.

One real, visible consequence of this ordering : `RelHttpRequest.jwt`, embedded into the request BEFORE Renew runs, always reflects the token AS VERIFIED — the claims actually presented for this request — never a renewal this same request happens to trigger. Renewal only ever changes `iat`/`exp` (never `role` or any custom claim), so a route function reading `req.jwt.role` or its own custom claims sees the same values either way ; only `req.jwt.iat`/`req.jwt.exp` could in principle differ from what ends up on the (separately, later) renewed cookie sent back to the client.

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
- Takes the full claims object as `jsonb` — deliberately not the `RelHttpRequest` domain, so it can never be picked up by `rpc.md`'s own route auto-discovery rule regardless of its name.
- Signals rejection the same way HTTP route functions do: `raise exception ... using errcode = 'RSxxx'` aborts the request with that status and clears the JWT cookie, forcing re-authentication. Returning normally means the session stands.
- Rel does not interpret or require any particular claim shape for revocation checks (e.g. a `jti`/session-id claim to look up) — that's an application convention on top of the generic `jsonb` payload.

# Roles

Much like PostgREST, rel sets a role on every request made to Postgres. Authenticating a user generally means assigning them a role in the JWT, which is then applied to every database request the session makes (escaped as an identifier, never interpolated raw).

Anonymous access is opt-in, not ambient : it's either anonymous, or it's verboten — there is no third, implicit "public" state a deployment falls into by simply not thinking about it. See `## Anonymous role existence` below for what actually governs this.

## Configuration

* `pg.query.anonymous_role` (default `~anonymous`) : the role rel applies when there is no JWT, or the one presented is missing, invalid, expired, session-checked out, or otherwise unusable. Same setting `query-engine.md ## Configuration` names — defined once, here, since this doc is where its behavior actually lives ; `query-engine.md`'s own entry should carry this same default rather than leaving it unstated (previously a genuine drift : this doc used to name the identical setting `jwt.anonrole`, a name that never matched `query-engine.md`'s `pg.query.anonymous_role` at all).

## Anonymous role existence

`pg.query.anonymous_role`'s default (`~anonymous`) is a naming convention, not itself a security posture. What actually governs whether anonymous access exists at all is whether a role by that name — or whatever the setting is explicitly configured to — is FOUND TO EXIST in the database, checked at introspection time (startup, and any future schema reload — see `specs/TODO.md`'s reload entry ; until reload exists, "checked at startup" is the same limitation every other piece of rel's introspected schema cache already has, not a new one).

This is deliberately a database-existence check, not a config-emptiness check : an operator who sets `pg.query.anonymous_role` to a custom name but forgets to `CREATE ROLE` it gets exactly the same protection, and the same diagnostic, as one who leaves the setting at its default and never creates `~anonymous` at all. Gating on "is the config value empty" instead would miss that first case entirely — the request would sail through to `SET ROLE` at request time and fail there instead, with a confusing runtime error rather than a clear one at startup.

* **Found to exist** : anonymous access is enabled, unchanged from every other behavior this document already describes.
* **Not found** : rel logs a warning at startup/reload (`configured anonymous role %q does not exist — all anonymous requests will be denied`) and proceeds with anonymous access disabled. NOT a fatal error — "no anonymous access at all" is a common, often deliberate, always-valid configuration, not a broken one. Same non-fatal-but-warn treatment `rpc.md`'s own route discovery already gives `http.response_domain_name` not resolving to anything — an established pattern for "a configured name that didn't resolve," reused here rather than inventing a new severity tier for this one case.

With anonymous access disabled, every unauthenticated request — to `/rel` AND `/rpc` alike — is rejected with `401`, immediately : before `/rpc`'s route lookup, before any request body is read, before a pool connection is ever acquired. There is nothing for such a request to fall through to : `SET ROLE ""`/`SET LOCAL ROLE ""` is a Postgres syntax error, and skipping the role switch entirely would silently run the request as whatever role the pool connection already has — a privilege escalation for anonymous callers. This was already a hard-stop path before this check existed (previously reachable only via an empty `pg.query.anonymous_role` and surfaced as a `500`, treated as a misconfiguration) ; it's now recognized as a first-class, intentional policy instead, reachable by simply not creating the role, and surfaced as a clean `401`.

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go; no reason to replace it.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `index.md`'s requirement of an easily configurable, generic approach — `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.

Each configured endpoint (SAML IdP, OIDC issuer, ...) is a named entry under its own config namespace (`saml.<name>.*`, `openid.<name>.*`), not detailed further here.
