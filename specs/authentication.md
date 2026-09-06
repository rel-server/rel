# JWT

## Claims

```typescript
type JWT = {
  role: string,
  iat: number,
  exp: number,
  auth_time: number,
} & {[name: string]: unknown}
```

- `role` — Required; a JWT without one is an error.

## Configuration

* `jwt.secret` (default `$FILE$/secrets/jwt-secret:./jwt-secret$GEN$32`) : Flat filename directly under `/secrets/`, not a subdirectory — rel never creates directories, only files, when a candidate path's parent already exists, and a flat path only requires `/secrets/` itself (the mounted volume) to exist. See `specs/configuration.md ## $GEN$ multi-path resolution` for what the colon-separated path list means.
* `jwt.algorithm` : rel enforces this exact algorithm on verification and rejects any token whose header claims a different one — including `none` — as an invalid signature.
* `jwt.max_age` : `exp = iat + jwt.max_age`. A function can override this for its own response via `jwt_attrs.maxage` (see Responses below).

The JWT cookie's `Max-Age` always mirrors the token's own `exp - iat` — it is never set independently. `http.cookies_max_age` (see `route.md ## Configuration`) does not apply to the JWT cookie; it only governs other, non-JWT cookies set via the generic `cookies` field.

## Lifecycle

1. **Mint.** A function establishes a session by setting the `jwt` field on its `RelHttpResponse` (see `route.md ## Responses`). `exp = iat + (jwt_attrs.maxage or jwt.max_age)`, and the cookie's `Max-Age` mirrors that same value.
2. **Verify.** On every subsequent request, rel checks the cookie's signature, `jwt.algorithm`, `exp`, and `auth_time + jwt.max_session_age`.
3. **Check.** If the token verifies, and `http.functions.check_session` is configured, rel calls it (see Session invalidation below).
4. **Renew.** rel re-mints it: fresh `iat`/`exp`, using the SAME width the current token's own `exp - iat` already has, fresh `Set-Cookie`, other claims carried over as-is. The renewed cookie's `SameSite` is always `jwt.same_site` — a `jwt_attrs.samesite` override from the original mint does NOT persist across renewal. Tokens under the `renew_after` threshold pass through unchanged.
   > Why: reusing the current token's own width means a `jwt_attrs.maxage` override from the original mint keeps applying across renewals without rel needing to remember it separately — the token's own claims are the only state involved. `SameSite` isn't reapplied because it isn't a JWT claim rel could carry forward without adding a private claim for one rarely-used cookie attribute. Passing unchanged tokens through bounds `Set-Cookie` churn to roughly once per `maxage × renewafter` of activity rather than once per request.
5. **Apply role.** Only now does rel apply `"<role>"` (role name escaped as an identifier) for the rest of the request — `/route` runs `SET LOCAL ROLE`, transaction-scoped, since its whole request shares one transaction start-to-finish ; `/rel` runs session-scoped `SET ROLE`/`RESET ROLE` on its pinned connection instead, since it commits its write transaction and then runs every item's read query AFTER that commit, still on the same connection (`specs/TODO.md`'s connection-pool/lifecycle entry).

**Request handling order, and what it means for `RelHttpRequest.jwt`.** On `/route`, Verify (step 2) runs first, needing no DB connection. Then the anonymous-access/route-authorization checks (`## Anonymous role existence` below, `route.md ## Anonymous route authorization`) run, then the request body is fully read and `RelHttpRequest` is built — all BEFORE a pool connection is acquired. Only then does the connection get acquired, `## Session invalidation`'s Check run, Renew (step 4) happen, and Apply role (step 5) apply.

> Why: this ordering means a request already known to be unauthorized, or malformed/oversized, never holds a pool connection open and never costs a connection-acquire round trip. `/rel` already reads and resolves its own request body before acquiring a connection for the same reason.

`RelHttpRequest.jwt`, embedded into the request BEFORE Renew runs, always reflects the token AS VERIFIED — the claims actually presented for this request — never a renewal this same request happens to trigger. Renewal only ever changes `iat`/`exp` (never `role` or any custom claim), so a route function reading `req.jwt.role` or its own custom claims sees the same values either way ; only `req.jwt.iat`/`req.jwt.exp` could in principle differ from what ends up on the (separately, later) renewed cookie sent back to the client.

## Session invalidation

- Runs in the same connection/transaction as the rest of the request's setup, immediately *before* `SET LOCAL ROLE` — no extra round trip. Running before the role switch, and being `security definer`, lets it consult tables the eventual per-request role has no business reading directly (a revocation list, a `users.disabled` flag).
- Takes the full claims object as `jsonb`, not the `RelHttpRequest` domain, so it is never picked up by `route.md`'s own route auto-discovery rule regardless of its name.

# Roles

## Configuration

* `pg.query.anonymous_role` : the identical setting `docs/content/configuration/index.md ### Postgres connection` names ; this document is the authoritative source for its behavior.

## Anonymous role existence

`pg.query.anonymous_role`'s default (`~anonymous`) is a naming convention, not itself a security posture. What governs whether anonymous access exists at all is whether a role by that name — or whatever the setting is explicitly configured to — is FOUND TO EXIST in the database, checked at introspection time (startup, and any future schema reload — see `specs/TODO.md`'s reload entry).

This is a database-existence check, not a config-emptiness check.

> Why: an operator who sets `pg.query.anonymous_role` to a custom name but forgets to `CREATE ROLE` it gets the same protection, and the same diagnostic, as one who leaves the setting at its default and never creates `~anonymous` at all. A config-emptiness check would miss that first case, letting the request sail through to `SET ROLE` at request time and fail with a confusing runtime error instead of a clear one at startup.

* **Not found** : rel logs a warning at startup/reload (`configured anonymous role %q does not exist — all anonymous requests will be denied`) and proceeds with anonymous access disabled. NOT a fatal error — "no anonymous access at all" is a common, valid configuration. Same non-fatal-but-warn treatment `route.md`'s own route discovery gives `http.response_domain_name` not resolving to anything.

With anonymous access disabled, rejection happens immediately : before `/route`'s route lookup, before any request body is read, before a pool connection is ever acquired.

> Why: `SET ROLE ""`/`SET LOCAL ROLE ""` is a Postgres syntax error, and skipping the role switch entirely would silently run the request as whatever role the pool connection already has — a privilege escalation for anonymous callers.

## Deployment prerequisite : role membership

`SET ROLE`/`SET LOCAL ROLE` only succeeds when the connecting role (`pg.query.user`, or `pg.user` when `pg.query.user` is unset) is a MEMBER of the role being switched to. This is ordinary Postgres privilege behavior, not something rel enforces or checks — it is the deploying developer's own responsibility to satisfy, and getting it wrong fails at request time, not at startup.

Skipping a grant doesn't fail at startup — introspection and dmut migrations both run under the PRIMARY connection (`pg.user`, never `pg.query.user`), so a missing grant is invisible until the first real request tries to `SET ROLE` into the ungranted role.

PostgREST documents the identical prerequisite for its own `authenticator`/`web_anon` pattern.

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`.
  > Why: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `index.md`'s requirement of an easily configurable, generic approach. `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.
