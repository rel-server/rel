# Legacy Auth Subsystem Reference

Source files: `auth.go`, `jwt.go`, `env.go`, plus wiring in `main.go` and downstream
consumption in `pg.go` / `pg2.go`. Package `main` (module `sales-way.com/server`).

## 1. Purpose

This subsystem authenticates HTTP clients against a Postgres backend and represents
the result as a signed JWT stored in a browser cookie. It supports three login
sources (username/password, "external"/SSO-mapped identity, and admin
"impersonation"), delegates the actual credential check to Postgres functions in an
`auth` schema, mints a JWT carrying a Postgres **role name** as its core claim, and
provides a per-request middleware (`jwtMiddleware`, `pg.go:10`) that later code uses
to `SET LOCAL ROLE` on the database connection for that request. It does not itself
implement password hashing, user storage, or RBAC — all of that lives in the
`auth.*` SQL functions; this code is essentially a thin session/JWT layer in front
of them.

## 2. Environment variables

Read once at process start into package-level vars (`env.go:32-46`), via
`getenvOrDefault(...)` which returns the first non-empty var in the list, else the
final default argument (`env.go:12-24`).

| Go var | Env vars checked (in order) | Default | Effect |
|---|---|---|---|
| `DISABLE_PASSWD` | `SW_DISABLE_PASSWD` | `""` | Any non-empty value disables the `/auth/login` password flow entirely (§7). |
| `JWT_ACCESSTOKEN_EXPIRY` | `SW_JWT_ACCESSTOKEN_EXPIRY` | `""` | Controls JWT/cookie lifetime; parsed in `jwt.go:16-32` (§5). Note: README (`README.md:75`) refers to this as `SW_ACCESSTOKEN_EXPIRY` — **the variable name in the README does not match the code** (missing `JWT_`). |
| `JWT_SECRET` | `SW_JWT_SECRET`, then `PGRST_JWT_SECRET` | `""` | HMAC signing secret for JWTs. If still empty after env lookup, falls back to a file-based secret (§2.1). |
| `JWT_COOKIE` | `SW_JWT_COOKIE`, then `PGRST_JWT_COOKIE` | `"accesstoken"` | Name of the session cookie. Shares the PostgREST-compatible fallback var names so the same value can drive both PostgREST and this server. |
| `DB_ANON_ROLE` | `SW_DB_ANON_ROLE`, then `PGRST_DB_ANON_ROLE` | `""` | Role to use when no valid session cookie is present, instead of failing auth (§9). Also read independently in `websocket-session.go:65` with a *different* fallback default (`"@unauthenticated"` instead of `""`) and a different env-var precedence (only `PGRST_DB_ANON_ROLE`, not `SW_DB_ANON_ROLE`) — a minor inconsistency between the HTTP and websocket paths. |
| `DB_IMPERSONATOR_ROLE` | `SW_IMPERSONATOR_ROLE` | `""` | Postgres role that is allowed to impersonate other roles (§6). |

Not read in these three files but related: `SAML_IDP` (`SW_SAML_IDP`), `DEBUG_ENABLED`
(`SW_ENABLE_DEBUG`), `ENABLE_TS_SCHEMAS` (`SW_ENABLE_TS_SCHEMAS`) — out of scope here.

### 2.1 JWT secret file fallback (`auth.go:139-163`)

If `JWT_SECRET` is empty after the env lookup, `SetupAuth` calls
`jwtSecretFromPath(srv)`:
- Looks for `/app/secrets/JWT_SECRET` on disk.
- If missing: generates a random 64-character secret from the charset
  `a-zA-Z0-9-_` using `crypto/rand` (`auth.go:125-137`), writes it to that path with
  mode `0600`, and uses it for this process.
- If present: reads and uses its raw contents as the secret.
- This means a fresh secret is generated **per container/volume** unless that path
  is persisted; if it isn't persisted, every restart invalidates all previously
  issued JWTs (users get logged out on redeploy).

## 3. HTTP endpoints registered by `SetupAuth` (`auth.go:170-382`)

All registered on `srv.Router` (chi router), unauthenticated at the router level
(these routes are *not* behind `jwtMiddleware`; they issue/consume the cookie
themselves).

| Route | Method(s) | Purpose | Notes |
|---|---|---|---|
| `/auth/endpoints` | GET | Lists enabled login mechanisms | Returns JSON array of `AuthEndpoint{name, displayname, endpoint}` filtered to entries whose `Hostname` matches `r.Host` or is empty (`auth.go:194-207`). Populated by `authEndpoints` global, appended to by this file (`_basic`) and presumably by OAuth/SAML setup elsewhere. |
| `/auth/login` | GET, POST | Username/password login | See §4.1. Any other method → 405. |
| `/auth/am-i-impersonator` | GET | Reports whether current session is an impersonation session | Returns literal body `"true"`/`"false"`; 401 if no/invalid cookie. |
| `/auth/impersonate/{user}` | GET | Switch session to another role | See §6. `{user}` is a chi URL param, used verbatim as the new role name. |
| `/auth/logout` | GET | Logs out | See §8. |
| `/test` | GET | Debug/smoke-test endpoint | Returns the resolved role as plain text, or 401. Not namespaced under `/auth`; looks like a leftover debug route. |

## 4. Login flows

Both password and external login ultimately call `jwtSetCookieForRole` (§5) with a
role name obtained from Postgres, then respond with `307 Temporary Redirect` to
`https://<r.Host>/` (no JSON body, no token in the response — the client must rely
on the cookie).

### 4.1 Password login (`auth.go:212-274`)

1. Rejected with `403` immediately if `disabledPasswords` is true (§7).
2. Credentials are read either from a JSON body (`POST`, `{"username","password"}`)
   or from query-string parameters `username`/`password` (`GET`) — **the GET path
   puts the password in the URL**, which will land in server access logs, browser
   history, and `Referer` headers (`auth.go:239-241`).
3. Acquires a pooled Postgres connection and immediately runs `RESET ROLE`
   (`auth.go:254-256`) before doing anything else — defensive reset in case the
   pooled connection retained a `SET ROLE` from a previous request's usage.
4. Calls `SELECT auth.login($1::text, $2::text)` (`auth.go:258`), passing raw
   username/password as SQL parameters. The DB function is expected to verify the
   credentials itself and return a **role name** (text) on success, or raise /
   return null on failure.
5. On any error (connection, exec, or scan failure) — including a failed login —
   replies `401` with `err.Error()` as the "short" message and empty description
   (`auth.go:272-273`). **The raw Postgres error message is echoed back to the
   client**, which can leak information about why the login failed.
6. On success: `jwtSetCookieForRole(srv, r, rw, role, impersonator=false)`, then
   `307` redirect to `https://<host>/`.

### 4.2 External / SSO login (`auth.go:41-75`, `doExternalLogin`)

Not itself bound to a route in this file — it's a helper called by other setup code
(OAuth/SAML handlers) once an external identity has been established, passing in a
`user` identifier string.

1. Gated by `srv.AvailableLoginFunctions.ExternalLogin`; if the DB doesn't expose a
   matching `auth.external_login` function, replies `403`.
2. Calls `SELECT auth.external_login($1)`, scanning into a nullable `pgtype.Text`.
   Null result or scan error → `401` ("User not found" or the raw error).
3. On success: same `jwtSetCookieForRole(..., impersonator=false)` +307 redirect
   pattern as password login.

### 4.3 Detecting which login functions exist (`ReloadLoginFunctions`, `auth.go:88-123`)

At startup (and whenever triggered again, see below), the server introspects
`information_schema.routines`/`parameters` to detect three specific SQL function
signatures in the `auth` schema:
- `auth.login(text, text) returns text`
- `auth.external_login(text) returns text`
- `auth.session_check` with **either** signature `(text, text, json) returns text`
  **or** `(text, jsonb) returns text` (`auth.go:88-107`) — two mutually-incompatible
  accepted shapes, which is unusual and easy to get subtly wrong when writing the
  DB-side function.

Results are stored in `srv.AvailableLoginFunctions{Login, ExternalLogin,
SessionCheck}` (`sw/defs.go:28-35`) and gate behavior throughout: password login is
disabled if `Login` is false (in addition to the `DISABLE_PASSWD` switch); external
login 403s if `ExternalLogin` is false; and `SessionCheck` toggles a live
per-request DB call (§4.4).

This reload is re-run: at boot inside `SetupAuth` (`auth.go:172`), and on
`SIGUSR1` after a successful `dmut` re-run (`main.go:307-312`), i.e. an operator can
push new/changed `auth.*` functions and signal the process to pick them up without
a full restart.

### 4.4 Optional `session_check` hook

If `AvailableLoginFunctions.SessionCheck` is true, two extra DB round-trips are
wired in:
- **At login/impersonation** (`jwtSetCookieForRole`, `jwt.go:253-291`): before
  minting the JWT, calls `SELECT auth.session_check(null, $1::text, $2::json)`
  with the role and the full set of request headers (JSON-marshalled) as
  arguments, and expects a session identifier string back, which is stored in the
  JWT's `s` claim.
- **On every protected request** (`jwtMiddleware`, `jwt.go:224-244`): calls
  `SELECT auth.session_check($1, $2::text, $3::json)` with the JWT's session id,
  role, and the current request's headers; any error → `401`. This is a synchronous
  DB call on the hot path of every authenticated request when this feature is
  enabled — a performance-sensitive design choice to be aware of.

## 5. JWT and cookie mechanics

### 5.1 Token contents (`jwt.go:40-45`)

```
PgJwtClaims {
  StandardClaims: { exp, iat }   // no `iss`, `aud`, `sub`, `nbf`
  impersonator bool  "impersonator"
  role         string "role"
  s            string "s"        // session id from auth.session_check, empty if unused
}
```

- Signed with `HS512` using `JWT_SECRET` (`jwt.go:80-81`).
- Verification (`jwtGetMapClaims`, `jwt.go:84-113`) only checks that the signing
  method is *some* HMAC variant (`*jwt.SigningMethodHMAC`), not specifically
  HS512 — a minor looseness (any HMAC alg the secret validates for is accepted),
  plus the library's own `Claims.Valid()` (checks `exp`/`nbf`/`iat` if present, no
  audience/issuer to check since none are set).

### 5.2 Role resolution helpers (`jwt.go:116-169`)

- `jwtGetRole` / `jwtGetImpersonatorFromRequest`: pull the corresponding claim out
  of a validated token; error if missing/wrong type.
- `jwtGetRoleFromRequest`: if there is **no cookie at all**, or the cookie fails to
  parse, falls back to `DB_ANON_ROLE` when that env var is set, otherwise errors.
  This is the anonymous-role fallback used by `/test`, `/auth/logout`, and the
  pg2.go request handler (`pg2.go:43`) that ultimately does `SET LOCAL ROLE` for
  the actual data API.

### 5.3 Cookie attributes (`jwtSetCookieAttrs`, `jwt.go:171-179`)

Every session cookie (name = `JWT_COOKIE`, default `accesstoken`) is set with:
- `Path=/`
- `HttpOnly=true`
- `Secure=true` (cookie will be dropped by browsers over plain HTTP — no dev-mode
  exception in code)
- `SameSite=Lax`
- `Expires` set to `now + JWT_DURATION`, **only if `JWT_DURATION > 0`**; otherwise
  the `Expires` field is left zero-valued, producing a browser session cookie.
- No `Domain` attribute is set (defaults to the exact request host, no subdomain
  sharing).

### 5.4 Expiry duration parsing (`init()` in `jwt.go:16-32`)

`JWT_DURATION` (package var, process lifetime) is computed once at startup from
`JWT_ACCESSTOKEN_EXPIRY`:
1. If empty → **default `365 days`** (`time.Hour * 24 * 365`).
2. Else, try `strconv.Atoi` on the string and treat the integer as **seconds**
   (`time.Second * time.Duration(dur)`).
3. Else, try `time.ParseDuration` (Go duration syntax, e.g. `"24h"`, `"30m"`).
4. Else, log "Invalid JWT duration" and fall back to 365 days.

**Discrepancy vs README**: `README.md:75-78` documents this variable (under the
wrong name, see §2) as being in **hours**, and states that if unset "the session
expires as soon as the browser closes." Neither is what the code does: a bare
integer is seconds, not hours, and the *default* when unset is a 365-day
persistent cookie, not a session cookie. A true session-cookie (`Expires` unset)
only happens if `JWT_ACCESSTOKEN_EXPIRY` is explicitly set to `"0"`.

### 5.5 Refresh-on-every-request middleware (`jwtRefreshCookieMiddleware`, `jwt.go:181-196`)

Registered globally via `srv.Router.Use(jwtRefreshCookieMiddleware)` (`main.go:147`,
i.e. before route matching, applied to *every* request including unauthenticated
ones). Behavior:
- If `JWT_DURATION <= 0`, this middleware is a no-op passthrough (returns `next`
  directly, not even wrapped) — since there's no expiry to refresh.
- Otherwise, on every request: if the `JWT_COOKIE` cookie is present, it re-applies
  `jwtSetCookieAttrs` (bumping the **cookie's** `Expires` to `now + JWT_DURATION`
  again) and re-sends the cookie — using the exact same token string (`r.Cookie()`
  only carries Name+Value; nothing is re-minted or re-signed).

  **This does not extend the session.** The JWT's own `exp` claim is fixed at mint
  time in `jwtCreateToken` (`jwt.go:49-50,74-77`) to `login time + JWT_DURATION`,
  and `jwt.Parse` (used by `jwtGetMapClaims`, invoked from `jwtMiddleware` on every
  protected request) enforces that claim on every check. So there are **two
  independent, decoupled expiry clocks**: the cookie's browser-side `Expires`
  (sliding — reset on every request) and the JWT's `exp` claim (absolute — fixed at
  login, never refreshed). The middleware only slides the former. Once wall-clock
  time passes `JWT_DURATION` since the original login, the JWT itself is expired
  and every `jwtMiddleware`-protected request starts returning `401`, even though
  the browser is still faithfully sending a cookie whose `Expires` has been kept
  perpetually in the future by this middleware. The practical effect is a user who
  stays continuously active still gets logged out after `JWT_DURATION` and must hit
  `/auth/login` (or an SSO/impersonation route) again to get a freshly-signed
  token — the "refresh" middleware doesn't prevent this, it just means the failure
  mode is a 401 rather than a clean "no cookie present" state.

### 5.6 `jwtMiddleware` (route-guarding middleware, `jwt.go:198-250`)

Applied to the actual data-API routes via `SetupPG` (`pg.go:8-19`), *not* to the
`/auth/*` routes themselves. For a protected request:
1. No cookie → `401`.
2. Cookie present but claims invalid/unparseable → `401`.
3. `role` claim missing/not a string → `401`.
4. If `SessionCheck` is enabled, `s` claim missing → `401`; then live
   `auth.session_check(session, role, headers)` call, any error → `401` (§4.4).
5. On success, stores `role` in the request context (`jwtRoleContextKey`) for
   downstream handlers (consumed later in `pg2.go:43` to `SET LOCAL ROLE`).

There is **no anonymous fallback** inside `jwtMiddleware` itself (unlike
`jwtGetRoleFromRequest`) — a missing cookie on a route guarded by `jwtMiddleware`
is always a 401, regardless of `DB_ANON_ROLE`.

## 6. Impersonation (`/auth/impersonate/{user}`, `auth.go:293-340`)

Mechanics:
1. Requires an existing valid `JWT_COOKIE` on the request; reads its claims
   directly (does not go through `jwtMiddleware`, so this route is reachable
   without ever being wrapped by that middleware — it does its own inline check).
2. Requires `claims["role"]` (string) and `claims["impersonator"]` (bool) to both be
   present and the latter to be `true`; otherwise `401`.
3. If authorized, takes the new role **verbatim from the URL path segment**
   `{user}` — no validation that this string is an actual Postgres role, no
   allow-list, no lookup.
4. Calls `jwtSetCookieForRole(srv, r, w, role, impersonator=true)` — note the
   `impersonator` argument passed is a **hardcoded `true`**, not re-derived from
   permissions of the new role.
5. Responds with a `307` redirect to `https://<host>/`, same as login.

How the `impersonator` claim actually gets set on a normal login
(`jwtCreateToken`, `jwt.go:48-82`): if `DB_IMPERSONATOR_ROLE` is configured **and**
the caller didn't already pass `impersonator=true`, it runs
`SELECT pg_has_role($1, $2, 'member')` against the target `role` and
`DB_IMPERSONATOR_ROLE`, and uses that boolean as the claim. This check is only
performed for "fresh" logins (password/external), because the impersonation
endpoint explicitly passes `impersonator=true` and thereby **skips this check
entirely** (the `&& !impersonator` guard, `jwt.go:52`).

Why this matters for security (this is the code-level reasoning behind the
README's "don't use in production" warning at `README.md:71`, going beyond what
the README states):

- **Chained impersonation without re-authorization.** Because step 4 above always
  passes `impersonator=true` for the *newly minted* cookie, a user who successfully
  impersonates role `B` receives a fresh JWT for `B` that **also carries
  `impersonator: true`**, regardless of whether `B` itself is a member of
  `DB_IMPERSONATOR_ROLE`. That JWT can then be used to impersonate a third role
  `C`, and so on — the impersonation privilege propagates through the chain rather
  than being re-checked against Postgres each hop.
- **Long-lived, baked-in privilege.** `DB_IMPERSONATOR_ROLE` membership is checked
  once, at the moment a JWT is minted (login time), not per-request. Because the
  default token lifetime is 365 days (§5.4) and there is no revocation list,
  revoking a user's membership in `DB_IMPERSONATOR_ROLE` in Postgres does **not**
  revoke the `impersonator: true` claim already baked into any JWT that user is
  currently holding — that only takes effect the next time they log in fresh.
  This is almost certainly what the README means by "the user must be a member of
  the role *and must have logged in after* the variable was set": `DB_IMPERSONATOR_ROLE`
  is itself a process-startup-time env var, so a deploy/restart that sets it only
  affects logins that happen after that restart; pre-existing sessions are
  unaffected either way until they log in again.
- **No validation that the impersonated role exists.** The `{user}` path segment is
  used as-is. The actual failure (if `user` isn't a real role) only surfaces later,
  downstream, when a data-API request tries to use it.
- **Unsanitized role name reaches raw SQL downstream.** The role stored in the JWT
  (whether from login, external login, or this arbitrary impersonation string) is
  later interpolated directly into SQL text: `pg2.go:62` builds
  `` SET LOCAL ROLE "<role>"; SET LOCAL "app.current_role" = '<role>'; `` via plain
  Go string concatenation (quoted, but not parameterized/escaped). A role/user
  string containing a `"` or `'` could break out of the quoting. Since
  `/auth/impersonate/{user}` lets an authorized impersonator choose this string
  freely, this is a direct path from an HTTP path parameter to unescaped SQL text.
- **CSRF-shaped surface.** The endpoint is a `GET` and changes session state
  (issues a new cookie for a different role) purely from being visited — no CSRF
  token, no `POST`, no confirmation step. Same is true of `/auth/login` (GET
  variant) and `/auth/logout`. An impersonator's browser following an
  attacker-supplied link (or a link-preview crawler) could silently switch which
  role their session is acting as.

Related read-only endpoint: `GET /auth/am-i-impersonator` lets the frontend check
`impersonator` claim without decoding the JWT client-side.

## 7. Disabling password login (`SW_DISABLE_PASSWD`)

`auth.go:174-188`: if `DISABLE_PASSWD` (from `SW_DISABLE_PASSWD`) is **any
non-empty string** (no special-casing of `"false"`/`"0"`/etc. — literally any
non-empty value, including the string `"false"`, disables it):
- `disabledPasswords = true`.
- A warning is logged.
- The `_basic` entry is **not** appended to `authEndpoints`, so `/auth/endpoints`
  won't advertise password login as available.
- The `/auth/login` handler is still registered and reachable, but immediately
  returns `403` ("Password authentication is disabled") for every request
  (`auth.go:220-223`).

Password login is also disabled (independently of this switch) if the DB doesn't
expose a matching `auth.login` function, logged as an error rather than a warning
(`auth.go:178-181`).

## 8. Logout (`GET /auth/logout`, `auth.go:345-367`)

1. Calls `gothic.Logout(rw, req)` (the `goth`/`gothic` OAuth library's own
   session/cookie cleanup for OAuth/SAML provider sessions).
2. Attempts `jwtGetRoleFromRequest(req)` — note this can succeed via the
   `DB_ANON_ROLE` fallback even with **no cookie at all**, in which case it will go
   on to call `auth.logout(<anon role>)`, which is likely a no-op but is a slightly
   odd code path to hit on every anonymous logout call.
3. If a role was resolved, best-effort calls `SELECT auth.logout($1)` on that role,
   ignoring the returned row/error entirely (`_ = conn.QueryRow(...)`) — this is
   purely advisory, "tell the DB we're logging out," per the comment at the top of
   `auth.go:7`.
4. A `// FIXME perform SAML logout.` comment (`auth.go:361`) indicates SAML-side
   logout (SLO) is not implemented.
5. Unconditionally clears the cookie via `UnsetCookie` (`auth.go:77-86`: sets an
   empty-value cookie with `Path=/`, `HttpOnly=true`, `Expires` at Unix epoch — note
   this does **not** set `Secure` or `SameSite`, unlike `jwtSetCookieAttrs`; in
   practice browsers generally still honor the deletion via matching name+path,
   but it's an inconsistent cookie-attribute story between "set" and "unset"
   paths).
6. Responds `200` with literal body `{"result":"ok"}` and no explicit
   `Content-Type` header set (so it's sent as whatever Go's default sniffing picks,
   not guaranteed to be `application/json`).

## 9. Error handling and status codes

All auth errors go through `ReplyError(w, code, short, desc)` (`error_pages.go:11-22`),
which renders an HTML error page (with escaped `short`/`desc`) and sets
`Cache-Control: no-cache, no-store, must-revalidate` — i.e. **auth errors return
HTML, not JSON**, even for what are effectively API endpoints. Status codes used
across this subsystem:

- `400` — malformed JSON body on `/auth/login` POST.
- `401` — bad credentials; missing/invalid/expired JWT cookie; missing role/session
  claim; failed `session_check`; external login user not found; impersonation
  claims missing or `impersonator != true`.
- `403` — password login disabled (`SW_DISABLE_PASSWD` or missing DB function);
  external login disabled (missing DB function).
- `405` — `/auth/login` called with a method other than GET/POST.
- `307` — successful login/external-login/impersonation (redirect to `https://<host>/`).
- `500` — cookie-setting failure inside `doExternalLogin` only (`auth.go:70`); the
  password-login handler's equivalent failure path instead falls through to the
  generic `401 erred:` label (`auth.go:265-267,272-273`), so a JWT-signing error on
  password login is reported as `401 Unauthorized` rather than `500`, which is
  misleading for debugging.

Anonymous fallback (`DB_ANON_ROLE`) is applied inconsistently: it's used by
`jwtGetRoleFromRequest` (hence by `/test`, `/auth/logout`, and the data-API request
path in `pg2.go`), but **not** consulted anywhere in `/auth/login`,
`/auth/impersonate`, or `jwtMiddleware`'s own cookie/claims checks.

## 10. Notes / things to reconsider for the rewrite

- **README/code drift.** The env var is `SW_JWT_ACCESSTOKEN_EXPIRY` in code but
  documented as `SW_ACCESSTOKEN_EXPIRY` in the README; the unit is seconds (bare
  integer) or a Go duration string, not hours as documented; and the "no value =
  session cookie" claim is false — the actual default is a 365-day persistent
  cookie. Whatever the new design does, keep the docs and the parsing in sync, and
  decide deliberately whether "unset" should mean session-cookie or long-lived.
- **Impersonation privilege propagates without re-checking DB membership** once the
  first hop is authorized (§6) — a design flaw, not just a "don't use in prod"
  footnote. A rewrite should re-verify role membership (or scope impersonation
  depth/whitelist) on every hop, and probably not let the impersonated JWT itself
  carry a fresh `impersonator: true` at all unless the target role is independently
  a member of the impersonator group.
- **Impersonation target is an unvalidated, freely-chosen string** taken from a URL
  path segment, later interpolated into raw SQL (`pg2.go:62`) with manual quoting
  instead of a parameterized identifier. This is a SQL-injection-shaped risk that's
  reachable by anyone already holding impersonator rights, and should use `pgx`'s
  identifier-quoting helpers (`pgx.Identifier{...}.Sanitize()`) or a parameterized
  `SET ROLE` mechanism at minimum.
- **State-changing GET endpoints.** `/auth/login` (GET variant), `/auth/logout`,
  and `/auth/impersonate/{user}` all mutate session state via `GET`, with no CSRF
  protection. `/auth/login` via GET also puts the password in the URL/query
  string, hitting logs and browser history.
- **Raw DB error strings surfaced to clients** on login failure (`auth.go:272-273`,
  `auth.go:64`) — a potential information-disclosure vector (e.g. distinguishing
  "user not found" from "wrong password" style Postgres errors, or leaking schema
  details from unexpected errors).
- **JWT secret persistence.** The file-based fallback secret
  (`/app/secrets/JWT_SECRET`) is generated per-process if the path isn't backed by
  a persistent volume, silently invalidating every session on redeploy with no
  warning to end users beyond a log line.
- **`jwtRefreshCookieMiddleware` slides the wrong clock** (§5.5): it extends the
  cookie's browser-side `Expires` on every request but never re-mints the JWT, so
  the JWT's own `exp` claim stays absolute from login time regardless of activity.
  In effect the "refresh" middleware does nothing useful — an active user still
  gets hard-logged-out (as 401s, not a clean missing-cookie state) after
  `JWT_DURATION` from their original login. A rewrite should either genuinely
  re-sign a fresh token with a new `exp` on activity (true sliding expiration) or
  drop the middleware and rely on absolute expiration + explicit refresh, but not
  both half-implemented at once. Note also `jwtGetMapClaims` reports the fixed
  string `"jwt: did not sign properly"` for *any* parse failure (`jwt.go:99-101`),
  including plain expiry — so expired-token cases get logged/misdiagnosed as
  signature problems. And the explicit `tok.Claims.Valid()` call right after
  (`jwt.go:103-106`) is redundant, since `jwt.Parse` already runs claim validation
  internally before returning.
- **Startup can nil-pointer-panic on a transient DB hiccup.** `ReloadLoginFunctions`
  logs the error from `PgSimpleQueryRow` but does not `return` on failure
  (`auth.go:110-113`), then unconditionally calls `rows.Scan(...)` at
  `auth.go:114`. `PgSimpleQueryRow` returns `(nil, err)` on a pool-acquire failure
  (`sw/defs.go:94-106`), so a transient connection issue during `SetupAuth` crashes
  the process on a nil `rows`.
- **`getenvOrDefault`'s docstring doesn't match its implementation.** The comment
  above it (`env.go:8-11`) claims it also checks
  `/var/run/secrets/<var>.txt` for Docker-style secrets, but the function body
  (`env.go:12-24`) does no filesystem access at all — only `os.Getenv`. Anyone
  configuring the legacy server based on that comment (e.g. mounting
  `SW_JWT_SECRET` as a Docker secret file) will find it silently ignored.
- **Stale/incorrect contract comment.** The file header (`auth.go:8`) documents
  `ext_login(uid text, email text, data json) returns text` as part of the
  required `auth` schema, but both the detection query (`auth.go:90`) and the
  actual call site (`auth.go:55`) use `auth.external_login(text)` — a single-arg
  function with a different name. The three-arg `ext_login` signature in the
  comment does not exist anywhere in the code.
- **Two major versions of `pgx` in one binary.** `auth.go:25-26` imports
  `jackc/pgtype` and `jackc/pgx/v4`, while `sw/defs.go:11-12` (and the pool itself)
  use `jackc/pgx/v5`/`pgxpool` v5. Worth resolving to a single major version in the
  rewrite.
- **Two handlers can return `200 OK` with a bare error string as the body,
  bypassing `ReplyError` entirely:** `doExternalLogin`'s pool-acquire failure
  (`auth.go:48-52`) and `/auth/logout`'s pool-acquire failure (`auth.go:351-355`)
  both do `fmt.Fprintln(rw, err)` with no preceding `WriteHeader` call, so Go
  defaults the status to `200`. A client cannot distinguish this from success by
  status code alone — it has to parse the body.
- **Two accepted signatures for `auth.session_check`** (`text,text,json` vs.
  `text,jsonb`) is an odd, easy-to-misconfigure DB contract (`auth.go:88-107`).
- **`Cookie.Secure = true` unconditionally**, no dev/local-HTTP escape hatch in
  code — fine for production but likely requires workarounds (self-signed TLS,
  `localhost` exceptions in some browsers) for local development; a rewrite might
  want an explicit `SW_INSECURE_COOKIES`-style dev flag instead of relying on
  browser quirks.
- **Inconsistent unset-cookie attributes vs. set-cookie attributes**
  (`UnsetCookie` vs. `jwtSetCookieAttrs`, missing `Secure`/`SameSite` on the former).
- **`DB_ANON_ROLE` fallback is inconsistently applied** across code paths (used in
  `jwtGetRoleFromRequest`/`pg2.go`/websocket path, not used in `jwtMiddleware`'s own
  checks or in `/auth/login`/`/auth/impersonate`), and the websocket module
  (`websocket-session.go:65`) re-reads the anon-role env var independently with a
  different default (`@unauthenticated`) and narrower fallback chain
  (`PGRST_DB_ANON_ROLE` only) than `env.go`'s `DB_ANON_ROLE` (`SW_DB_ANON_ROLE`,
  `PGRST_DB_ANON_ROLE`) — two sources of truth for the same concept.
- **`/test` debug endpoint** (`auth.go:369-380`) is unnamespaced, always registered,
  and simply echoes the resolved role — looks like a leftover development route
  rather than an intentional part of the API surface.
- **`AuthEndpoint.Hostname` field has no `json:"-"` tag**, so it will actually
  serialize into the `/auth/endpoints` JSON response (as `"Hostname":""` for the
  basic-auth entry) even though it's clearly meant as server-side-only filtering
  state (`auth.go:31-36`, used in `auth.go:200-204`).
- **Errors are rendered as HTML** (`error_pages.go`) even for what are effectively
  JSON/API endpoints under `/auth/*` — a client has to sniff status code + HTML
  body rather than parse structured JSON on failure.
- **All-HMAC-family acceptance in `jwt.Parse`'s key function** (`jwt.go:92-97`)
  checks for `*jwt.SigningMethodHMAC` generically rather than pinning to `HS512`
  specifically (the alg actually used to sign). Low risk since the secret is
  shared and symmetric either way, but worth being explicit/pinned in a rewrite.
