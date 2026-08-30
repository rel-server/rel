
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

**Request handling order, and what it means for `RelHttpRequest.jwt`.** On `/rpc`, Verify (step 2 above) runs first — it's what makes "is this request anonymous" knowable at all, and needs no DB connection. Then the anonymous-access/route-authorization checks (`# Roles ## Anonymous role existence`, `# HTTP ## Anonymous route authorization`) run, then the request body is fully read and `RelHttpRequest` is built — all of this BEFORE a pool connection is acquired. Only then does the connection get acquired, `## Session invalidation`'s Check run, Renew (step 4 above) happen, and Apply role (step 5) apply — deliberately, so a request already known to be unauthorized, or that turns out to be malformed/oversized, never holds a pool connection open at all, and never costs a connection-acquire round trip in the first place. `/rel` already reads and resolves its own request body before acquiring a connection for exactly the same reason ; `/rpc`'s ordering here brings it in line with that, not a new pattern.

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
- Takes the full claims object as `jsonb` — deliberately not the `RelHttpRequest` domain, so it can never be picked up by the HTTP route auto-discovery rule (see HTTP below) regardless of its name.
- Signals rejection the same way HTTP route functions do: `raise exception ... using errcode = 'RSxxx'` aborts the request with that status and clears the JWT cookie, forcing re-authentication. Returning normally means the session stands.
- Rel does not interpret or require any particular claim shape for revocation checks (e.g. a `jti`/session-id claim to look up) — that's an application convention on top of the generic `jsonb` payload.

# Roles

Much like PostgREST, rel sets a role on every request made to Postgres. Authenticating a user generally means assigning them a role in the JWT, which is then applied to every database request the session makes (escaped as an identifier, never interpolated raw).

Anonymous access is opt-in, not ambient : it's either anonymous, or it's verboten — there is no third, implicit "public" state a deployment falls into by simply not thinking about it. See `## Anonymous role existence` below for what actually governs this.

## Configuration

* `pg.query.anonymous_role` (default `~anonymous`) : the role rel applies when there is no JWT, or the one presented is missing, invalid, expired, session-checked out, or otherwise unusable. Same setting `querying.md ## Configuration` names — defined once, here, since this doc is where its behavior actually lives ; `querying.md`'s own entry should carry this same default rather than leaving it unstated (previously a genuine drift : this doc used to name the identical setting `jwt.anonrole`, a name that never matched `querying.md`'s `pg.query.anonymous_role` at all).

## Anonymous role existence

`pg.query.anonymous_role`'s default (`~anonymous`) is a naming convention, not itself a security posture. What actually governs whether anonymous access exists at all is whether a role by that name — or whatever the setting is explicitly configured to — is FOUND TO EXIST in the database, checked at introspection time (startup, and any future schema reload — see `specs/TODO.md`'s reload entry ; until reload exists, "checked at startup" is the same limitation every other piece of rel's introspected schema cache already has, not a new one).

This is deliberately a database-existence check, not a config-emptiness check : an operator who sets `pg.query.anonymous_role` to a custom name but forgets to `CREATE ROLE` it gets exactly the same protection, and the same diagnostic, as one who leaves the setting at its default and never creates `~anonymous` at all. Gating on "is the config value empty" instead would miss that first case entirely — the request would sail through to `SET ROLE` at request time and fail there instead, with a confusing runtime error rather than a clear one at startup.

* **Found to exist** : anonymous access is enabled, unchanged from every other behavior this document already describes.
* **Not found** : rel logs a warning at startup/reload (`configured anonymous role %q does not exist — all anonymous requests will be denied`) and proceeds with anonymous access disabled. NOT a fatal error — "no anonymous access at all" is a common, often deliberate, always-valid configuration, not a broken one. Same non-fatal-but-warn treatment `## HTTP`'s own route discovery already gives `http.response_domain_name` not resolving to anything — an established pattern for "a configured name that didn't resolve," reused here rather than inventing a new severity tier for this one case.

With anonymous access disabled, every unauthenticated request — to `/rel` AND `/rpc` alike — is rejected with `401`, immediately : before `/rpc`'s route lookup, before any request body is read, before a pool connection is ever acquired. There is nothing for such a request to fall through to : `SET ROLE ""`/`SET LOCAL ROLE ""` is a Postgres syntax error, and skipping the role switch entirely would silently run the request as whatever role the pool connection already has — a privilege escalation for anonymous callers. This was already a hard-stop path before this check existed (previously reachable only via an empty `pg.query.anonymous_role` and surfaced as a `500`, treated as a misconfiguration) ; it's now recognized as a first-class, intentional policy instead, reachable by simply not creating the role, and surfaced as a clean `401`.

# Authentication

- **SAML**: `github.com/crewjam/saml`, as in legacy — the de facto standard SP implementation in Go; no reason to replace it.
- **OpenID Connect / OAuth**: `golang.org/x/oauth2` for token exchange, plus `github.com/coreos/go-oidc/v3` for discovery (`.well-known/openid-configuration`) and ID-token verification. This replaces legacy's `goth`/`gothic`: goth is a catalog of hand-maintained, named per-provider packages (google, salesforce, a bespoke in-repo yahoo one — see `legacy-docs/oauth.md`), which doesn't fit `00-general.md`'s requirement of an easily configurable, generic approach — `go-oidc` speaks to any standards-compliant IdP given just its issuer URL, no per-provider Go package needed.
- **Username/password**: no library — credential verification is delegated to a Postgres function.

Each configured endpoint (SAML IdP, OIDC issuer, ...) is a named entry under its own config namespace (`saml.<name>.*`, `openid.<name>.*`), not detailed further here.

# HTTP

Routing is done with the standard library's `net/http.ServeMux` (Go 1.22+ method/wildcard patterns, e.g. `"GET /rpc/{schema}/{function}"` with `r.PathValue(...)`) — no external router dependency. This fits rel's actual routing needs: a small, fixed set of patterns (`/rpc/{schema}/{function}`, `/rel`, `/auth/*`, `/js/*`, static files), since individual database functions are dispatched dynamically from within the `/rpc/{schema}/{function}` handler rather than registered as their own routes. Verify and Renew (steps 2 and 4 — see `# JWT` above) need no database and are implemented as ordinary `func(http.Handler) http.Handler` middleware, composed by hand — no framework-specific request/context type involved. Check and Apply role (steps 3 and 5) need the request's own DB connection, which doesn't exist yet when generic middleware runs, so those two are each handler's own responsibility instead (`/rel`, `/rpc`) once a connection is acquired — see `specs/TODO.md`'s connection-pool/lifecycle entry.

> Why `/rpc`, not `/api` : `/api` describes nothing about what the route actually does (every HTTP endpoint in existence is "an API"). `/rpc` names the actual mechanism — a named backend function called directly over HTTP — and matches PostgREST's own convention for the identical concept, which `# Roles` below already invokes as a point of comparison.

Creating the domain types `RelHttpRequest` and `RelHttpResponse` on JSON or JSONB (their schema doesn't matter) and using them in function prototypes, or creating a domain over `bytea` OR `text` with a `/` in their name, enables functions to return binary (or plain-text) content with the domain name as mime-type — a "mimetype domain". Which underlying type a mimetype domain uses only changes how its return value becomes the response body : a `bytea`-underlying one's raw bytes ARE the body ; a `text`-underlying one's string IS the body directly, with no base64 or other encoding involved (unlike `RelHttpRequest.body`'s own binary case — see `## Request` below — a mimetype domain's return value is never itself wrapped in JSON, so there's no jsonb-safety concern forcing an encoding here). `"text/plain"` over `text` is the common case — deliberately expressed this way (a NAMED domain, same mechanism as `"image/png"`) rather than as a special case for a bare, undecorated `returns text`/`returns json` with no domain wrapper at all : a bare scalar return type carries no name to derive a `Content-Type` from, so it would need an invented default (`text/plain` is a defensible guess for bare `text` ; there's no equally obvious guess for bare `bytea`, which is exactly why mimetype domains exist in the first place — the type's OWN name states the content-type explicitly instead of rel guessing one).

A function whose name does **not** start with `_`, and that takes the argument shapes `## Request bodies` below describes (0 arguments, one `RelHttpRequest` argument, or `RelHttpRequest` plus one of the extra body-parameter shapes), and that returns `RelHttpResponse` or a mimetype domain, is a HTTP route function — callable directly at `/rpc/<schema>/<function_name>` (and, if `http.functions.allowed_routes` is set, only if its fully qualified name also matches that regexp). A leading `_` opts a function out of route discovery unconditionally, with no configuration needed — this is how internal helpers that happen to match the signature stay unexposed.

Such a function's name can end with `__VERB` (`__GET`, `__POST`, ...) to restrict which HTTP verb it answers to; case doesn't matter. If a verb-suffixed function is defined alongside an unsuffixed one, the unsuffixed function is the fallback for verbs with no specific match. Conforming to web semantics (e.g. `__GET` must not mutate state — see the CSRF note under Cookies) is the function author's responsibility; rel does not enforce it.

`RelHttpResponse` can render via [Jet templates](github.com/CloudyKit/jet) through the optional `template` key.

```sql
create domain "RelHttpRequest" as jsonb;
create domain "RelHttpResponse" as jsonb;
create domain "image/png" AS bytea;
create domain "text/plain" AS text;
create function schema.some_function(req RelHttpRequest) returns RelHttpResponse /* ... */;
create function schema.returns_binary() returns "image/png" /* ... */;
create function schema.returns_text() returns "text/plain" /* ... */;
```

## Configuration

* `http.request_domain_name` (default `RelHttpRequest`) : the fully qualified, unquoted name of the JSON domain for request-typed functions.
* `http.response_domain_name` (default `RelHttpResponse`) : the fully qualified, unquoted name of the JSON domain rel interprets as an HTTP response return type. Not an error if it doesn't exist, but rel will warn, since without it no authentication flow can work.
* `http.templatesdir` (default `/template`) : the template directory.
* `http.cookies_max_age` (default `86400`) : default max-age for cookies set via the generic `cookies` field, when the response doesn't specify one. Does not apply to the JWT cookie (see `jwt.max_age`).
* `http.functions.allowed_auth` (default empty) : regexp restricting which functions' responses rel will honor a `jwt` field from, matched against the fully qualified, unquoted function name. Empty means unrestricted.
* `http.functions.allowed_routes` (default empty) : regexp a function's fully qualified, unquoted name must additionally match to become a public route, on top of having the right signature and not starting with `_`. Empty means unrestricted (any matching-signature, non-`_` function is routed).
* `http.max_body_size` (default `10485760`, 10 MiB) : hard cap, in bytes, on a `/rpc` request's ENTIRE body — for multipart, the whole envelope (boundaries and part headers included, not just the sum of part payload bytes), a strictly tighter bound than "just the files" would be. Enforced before any of it is buffered in memory, not after — a request whose `Content-Length` already exceeds this (or whose body turns out to exceed it while streaming, for a chunked request with no declared length) is rejected outright (`413 Payload Too Large`), never partially read into memory first. Exists specifically so `## Request bodies`' multipart/binary support doesn't turn `/rpc` into an unauthenticated memory-exhaustion vector — every request body was already read into memory unconditionally before this setting existed, which was fine for small JSON payloads and not fine once arbitrary file uploads are in scope. 10 MiB is a starting point sized for "a handful of modest images/documents," not large media — a deployment doing genuine large-file uploads is expected to raise this explicitly, a deliberate choice rather than an accidentally-permissive default. Deliberately scoped to `/rpc` only : `/rel`'s own POST bodies (`querying.md`'s write payloads) are legitimately large for a bulk write and already a distinct code path — bounding those, if ever needed, is a separate setting/decision, not silently folded into this one.
* `http.max_part_count` (default `100`) : max number of `multipart/form-data` parts a single `/rpc` request may contain — see `## Request bodies ### Limits` for why this is a separate bound from `http.max_body_size`, not redundant with it.

## Anonymous route authorization

When anonymous access is enabled (`# Roles ## Anonymous role existence` above), rel additionally caches — at introspection/reload time, once per discovered route function, never per request — whether the anonymous role can actually call it : `has_schema_privilege(anonymous_role, schema, 'USAGE') AND has_function_privilege(anonymous_role, function, 'EXECUTE')`, BOTH conjuncts, matching exactly what Postgres itself checks at call time (`USAGE` on the schema is required to even reach the function, independent of `EXECUTE` on the function itself — checking `EXECUTE` alone would go optimistic on any schema that gates access via `USAGE`, which defeats the point for exactly the locked-down deployments this exists for ; go check either primitive alone and the cache can say "allowed" for a request that dies at the real check anyway, silently turning this back into the thing it was built to prevent). This reuses Postgres's own authority rather than inventing a parallel one — it already accounts for direct grants, `PUBLIC` grants, and role membership/inheritance correctly on both checks. An anonymous request to a route the anonymous role can't reach is rejected with `401` — before the request body is read, and before a pool connection is acquired — the same fail-fast reasoning as the existence check above, scoped one level narrower (per route, rather than "is anonymous access on at all").

This check is intentionally scoped to the ANONYMOUS role only, never every role in the database, for two independent reasons :

* Unauthenticated requests are the cheap, high-volume attack surface — anyone can send one, no valid JWT required. An authenticated request already implies possession of a signed session, a real (if imperfect) barrier that changes the cost/risk calculus enough that the existing live check at `SET LOCAL ROLE` + invocation time remains the right tradeoff for it — this fail-fast cache does not extend to authenticated requests at all.
* A deployment where each individual user has their own Postgres role (a common pattern — "1 user = 1 role") could have thousands to millions of distinct roles. Caching a full function × role matrix at that scale is a real memory and introspection-time cost, for a case (authenticated abuse) this mechanism was never trying to address in the first place. One cached boolean per route (anonymous only), nothing precomputed for any other role, ever.

Independent of the anonymous-role existence check, rel also warns — at introspection/reload — for every discovered route function that's actually reachable by `PUBLIC` : `has_schema_privilege('PUBLIC', schema, 'USAGE') AND has_function_privilege('PUBLIC', function, 'EXECUTE')`, the same two-conjunct check as above, for the same reason — a function with `PUBLIC EXECUTE` sitting in a schema without `PUBLIC USAGE` isn't actually publicly callable, and warning on it anyway would just teach operators to ignore the warning. Postgres grants `EXECUTE` to `PUBLIC` by default on `CREATE FUNCTION`, unless the schema's default privileges were altered or the grant explicitly revoked afterward — a well-known footgun independent of anything rel does, meaning a route can be silently callable by every role in the database (anonymous included, and every "1 user = 1 role" tenant alike) without the schema author ever intending it. Not fatal — revoking `PUBLIC` by default is a schema-authoring discipline rel can surface loudly but can't enforce.

Both checks here are `/rpc`-specific. `/rel`'s own relation-level access (`SELECT`/`INSERT`/`UPDATE`/`DELETE` grants, per column, further narrowed by rel's own scoping/blacklist logic — `querying.md ## Scoping`) is a fundamentally more nuanced shape than a single `EXECUTE` bit and isn't addressed by either check here.

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

If a function has exactly one argument of type `RelHttpRequest`, or `RelHttpRequest` plus one of the extra body-parameter shapes `## Request bodies` describes, the request is encoded as:

```typescript
interface RelHttpRequest {
  method: string
  uri: string
  query: unknown
  headers: {[name: string]: string[]}
  content_type: string
  body: unknown // shape depends on content_type — see below

  cookies: {[name: string]: string} // value only — see the note below
  jwt: JWT | null // null when the request carries no valid session
}
```

`RelHttpRequest.body` (renamed from an earlier, always-a-raw-string `content` field) is typed by `content_type`, the same way `RelHttpResponse.content` already lets its own `content_type` drive interpretation on the response side — this is that same convention, now also applied to the request side, not a new one :

* `application/json`, or any `+json` suffix (`application/vnd.api+json`, ...) : `body` decodes to the request's actual JSON value — an object, array, or scalar — never a JSON-encoded STRING of it. This is the change that actually motivated the rename : the old `content: string` forced a JSON request body through `(req->>'content')::jsonb` inside every function that wanted to use it, double-encoding for no reason.
* `text/*` (any subtype, `charset=` parameter ignored for this match) : `body` is the plain string, exactly as `content` used to always be.
* `application/x-www-form-urlencoded` (a plain `<form>` POST with no `enctype` — the default, used for any form with no file input) : `body` decodes to a JSON object, through the exact same structural dot-path decoder `query`/`GET /rel` already use (`querystring.DecodeStructural` — see `specs/query_json.md ## Structural layer`) applied to the body's own bytes instead of the URL's query string. `name=John&email=john%40example.com` becomes `{"name": "John", "email": "john@example.com"}` ; a dotted key nests the same way it already does for `query`/`GET /rel` (`user.email=...` → `{"user": {"email": "..."}}`). Reusing that decoder rather than inventing a second flat key-value parser is deliberate : `application/x-www-form-urlencoded` and a URL's own query string are the identical percent-encoded `key=value&key=value` syntax (same encoding, just carried in the body instead of after a `?`), so treating the body case as a second, separate thing to parse would just be duplicated logic with its own chance to drift from the first.
* Anything else (binary) : `body` is a base64-encoded string of the raw bytes — the only encoding that survives unmodified inside jsonb (a raw byte sequence isn't valid JSON text on its own, and a Go/JS/Postgres JSON encoder given arbitrary non-UTF-8 bytes as a "string" mangles them, replacing invalid sequences — silently corrupting the payload). Postgres's own `decode(body, 'base64')::bytea` recovers the original bytes inside the function body. Only reachable on a route that did NOT declare `## Request bodies`' `files bytea[]` parameter — see there for why a real upload should use that instead.
* No body at all (a `GET`, or any request with an empty body) : `body` is JSON `null`.
* A route declaring `## Request bodies`' `files bytea[]` parameter : `body` is ALWAYS JSON `null`, regardless of `content_type` — the payload is delivered exclusively through `files`/`parts_headers` in that case, never duplicated into `body` as well (a `(req, files bytea[])` route receiving a 10 MiB upload does NOT also carry a ~13 MiB base64 copy of the same bytes inside `req`).
* `application/json` whose body does not actually parse as valid JSON, or `application/x-www-form-urlencoded` whose body doesn't decode (a genuinely malformed percent-encoding, say) : `400`, same as any other malformed-request case (`## Postgres Exceptions` doesn't apply — this never reaches a function at all).

`RelHttpRequest.cookies` deliberately does NOT reuse the full `Cookie` shape (`value`/`httponly`/`secure`/`samesite`/`maxage`) the response side uses — a browser's `Cookie` header only ever sends `name=value`, the other four attributes are response-only (`Set-Cookie` attributes) and can never be known for an inbound cookie. Value-only avoids four fields that would always be empty/false/zero.

## Request bodies

`RelHttpRequest.body` above covers the common case — one body, JSON or text or a single binary blob. Real uploads (`multipart/form-data`, several independently-typed parts in one request, or a single raw binary `POST`) are expressed instead as EXTRA function parameters beyond `req RelHttpRequest`, matched by their TYPE SEQUENCE — never by parameter name, so a function author names them however they like (`files`, `attachments`, `uploads`, doesn't matter to rel) :

```sql
-- bytes only, no metadata
create function schema.upload(req RelHttpRequest, files bytea[]) returns RelHttpResponse /* ... */;
-- bytes plus full per-part metadata
create function schema.upload_with_headers(req RelHttpRequest, files bytea[], parts_headers jsonb) returns RelHttpResponse /* ... */;
```

Only these four shapes — `()`, `(req)`, `(req, files bytea[])`, `(req, files bytea[], parts_headers jsonb)` — are recognized route-function signatures — anything else (extra parameters, a different order, a different type) is simply not discovered as a route at all, same as any other signature mismatch today. There is deliberately no separate single-file-only shape (an earlier draft of this section had `body bytea`/`bodies bytea[]` as two distinct families, five shapes beyond `(req)` alone — `body`, `body,mime`, `bodies`, `bodies,names`, `bodies,names,mimes`) : a single raw binary `POST` is treated as a ONE-ELEMENT `files` array (see below) rather than its own mechanism, so `files bytea[]` alone already covers "exactly one file" — `files[1]` — with no dedicated singular form needed. This also collapses what used to be three separate mismatch rules (see below) into two.

`parts_headers`, when declared, is a JSON array, index `i` describing `files[i]` :

```typescript
interface RequestPart {
  name: string | null         // Content-Disposition name= (the multipart form field key) ; null for the synthesized pseudo-part below
  filename: string | null     // Content-Disposition filename= ; null when this part isn't a file (e.g. a plain form field)
  content_type: string | null // this part's own Content-Type header ; null if the part had no Content-Type header at all — NOT the empty string
  headers: {[name: string]: string[]} // ALL of this part's own headers, including Content-Type/Content-Disposition verbatim (redundant with content_type/name/filename above, same convenience-plus-raw relationship RelHttpRequest.content_type/.headers already have) — same shape RelHttpRequest.headers already uses
}
```

`name` and `filename` are kept as their own first-class fields (parsing `Content-Disposition`'s own parameters isn't something a function should have to redo in plpgsql), rather than folded into `headers` — though `headers` still legitimately also contains the raw `Content-Disposition`/`Content-Type` header values verbatim, same relationship `RelHttpRequest.content_type`/`.headers` already have with each other (one promoted for convenience, both present). Unlike the earlier draft's `names`-falls-back-to-the-field-key rule, `name` and `filename` are now always independently available — a route distinguishing two differently-named file inputs no longer loses the field key just because a filename is also present.

For a single, non-multipart, raw binary `POST` (a request whose `Content-Type` isn't `multipart/*` but also isn't JSON/text — the same set of requests that would otherwise populate `body` via `## Request`'s "anything else" branch), `files` is a one-element array holding the whole raw body, and `parts_headers` (if declared) is a one-element array holding a SYNTHESIZED pseudo-part : `name: null`, `filename: null`, `content_type` = the request's own `content_type`, `headers` = the request's own `headers` (there's no separate "part" envelope to have its own headers when there was no multipart wrapper to begin with).

A real multipart form frequently mixes plain (non-file) fields with file uploads in one submission (a `title` text input alongside an attached file, say) — nothing here drops them : a plain field still lands in `files` (as its raw UTF-8 bytes — `convert_from(files[i], 'utf8')` recovers the string inside the function), with `parts_headers[i].name` set to its field key and `parts_headers[i].filename` staying `null` (no `filename=` on a plain field). There's no structural separation between "this index is a real file" and "this index is a plain form value" beyond checking `parts_headers[i].filename IS NULL` — a function that cares about the difference makes that call itself.

Declaring `files bytea[]` (with or without `parts_headers`) is a hard contract about what the route accepts — a mismatch against the actual request is always a `415 Unsupported Media Type`, never a silent fallback :

* A route declaring `files` receiving a request whose `content_type` falls into `## Request`'s JSON, `text/*`, or `application/x-www-form-urlencoded` branches (i.e. `body` would otherwise have been populated) is a `415` — there is no byte payload to hand over as a "file" separately from what's already fully represented as `body`.
* A route NOT declaring `files` (a plain `(req)`-only route) receiving an actual `multipart/form-data` request is ALSO a `415` — multipart data has nowhere to go without a `files` parameter, and is never silently base64-encoded whole (boundaries and all) into `body` as a fallback.
* A route declaring `files`, called with NO body at all (no `Content-Type`, empty body — e.g. a `__GET`-suffixed variant of an upload route, or any request that simply omits one) : NOT a `415` — `files` is an empty array (`'{}'::bytea[]`), `parts_headers` (if declared) an empty JSON array. A route that genuinely requires at least one file checks `array_length(files, 1)` itself and raises its own `RSxxx` if that matters to it ; rel does not treat "declared `files` but got none" as a shape mismatch on its own; only a WRONG shape (JSON/text body on a `files` route) is.
* A syntactically valid `multipart/form-data` envelope containing zero parts is treated the same as the no-body case immediately above — empty `files`/`parts_headers`, not a `415` and not an error, on any route (whether or not it declared `files`) : an empty multipart body is a genuinely empty request, not a shape mismatch.

### Limits

* `http.max_body_size` (see `## Configuration` above) bounds total bytes.
* `http.max_part_count` (default `100`) bounds the number of multipart parts a single request may contain, independent of their total byte size — without this, a request built from a very large number of near-empty parts stays under `http.max_body_size` while still costing real CPU/memory in per-part header parsing and allocation, a distinct resource-exhaustion shape from the byte-size one. 100 is sized generously above what a genuine multi-file form submission needs (a handful to a few dozen files) while still bounding the cost of a pathological one ; exceeding it is a `413 Payload Too Large`, checked incrementally while parsing (a request is rejected as soon as the count is exceeded, not after fully parsing an oversized part set). A synthesized single-binary-POST pseudo-part always counts as exactly one part, trivially under any sane limit.

### Known limitation : no true streaming to Postgres

`files`/`body` both require the ENTIRE relevant payload to be fully received and held in memory (bounded by `http.max_body_size`) before the route function is ever invoked — there is no mechanism here for a function to receive a live stream, or for rel to write bytes to Postgres incrementally as they arrive over HTTP. Raising `http.max_body_size` only moves this ceiling ; it does not change the fact that the whole payload is buffered at once. This is not an oversight to silently work around later — genuinely large uploads (video, multi-GB archives) are not what `files`/`body` are for.

This is not an inherent limitation of Postgres itself, though : `pg_largeobject` (`lo_*` functions, exposed to Go via `pgx.LargeObjects`) supports genuine chunked read/write without full in-memory buffering on either side. Using it would be a materially different, separately-designed mechanism — an `oid` reference in place of `bytea`, its own ACL/ownership and orphan-cleanup lifecycle distinct from ordinary table storage, and rel's handler streaming HTTP bytes into it via a sequence of `lo_write` calls before the route function runs — not something this pass adds. Documented here as the answer to "is streaming possible at all," not as a promise of when.

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
