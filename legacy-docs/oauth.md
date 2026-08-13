# Legacy OAuth Subsystem

## 1. Purpose / role in the server

`SetupOAuth` (`/home/chris/Code/rel/_legacy/oauth.go:17`) wires up "login with a third-party
identity provider" on top of **goth** (`github.com/markbates/goth v1.64.2`, confirmed in
`/home/chris/Code/rel/_legacy/go.mod:21`), Mark Bates' Go OAuth1/OAuth2 provider abstraction
library, plus its companion package **gothic** (`github.com/markbates/goth/gothic`) which
supplies ready-made `net/http` handlers for the begin/callback dance and a session store for
the in-flight OAuth handshake.

The server does not implement any OAuth protocol logic itself. It only:
- registers zero or more goth `Provider` instances (one per configured provider **and** per
  virtual hostname the server serves — see §3),
- exposes `/oauth/{provider}/login` and `/oauth/{provider}/callback` routes that delegate to
  gothic,
- on a successful callback, takes the resulting `goth.User.Email` and hands it to the same
  application-level login machinery used by password/SAML login (`doExternalLogin`,
  `/home/chris/Code/rel/_legacy/auth.go:41`) to mint the app's own JWT cookie.

OAuth is one of three login front-ends in the legacy server (password login in
`SetupAuth`/auth.go, SAML in saml.go, OAuth here); all three converge on
`jwtSetCookieForRole` (`/home/chris/Code/rel/_legacy/jwt.go:253`) for issuing the session.

## 2. Environment variables

All OAuth provider credentials follow the pattern `SW_<PROVIDER>_KEY` / `SW_<PROVIDER>_SECRET`,
read directly with `os.Getenv` inside the `register` closure
(`/home/chris/Code/rel/_legacy/oauth.go:19-49`, values fetched at `oauth.go:21-22`). There is
**no fallback/default mechanism** for these (unlike e.g. `JWT_SECRET`, which also checks
`PGRST_JWT_SECRET` via `getenvOrDefault`).

| Variable | Provider | Required? | Behavior if absent |
|---|---|---|---|
| `SW_GOOGLE_KEY` / `SW_GOOGLE_SECRET` | Google | Both required together | Provider silently not registered (see below) |
| `SW_SFDC_KEY` / `SW_SFDC_SECRET` | Salesforce | Both required together | Provider silently not registered |
| `SW_YAHOO_KEY` / `SW_YAHOO_SECRET` | Yahoo | Both required together | Provider silently not registered |
| `SW_ORANGE_KEY` / `SW_ORANGE_SECRET` | Orange (OpenID Connect) | N/A | **Dead code** — registration call is commented out (`oauth.go:71-81`); never runs |
| `SESSION_SECRET` (no `SW_` prefix — read by the **goth/gothic** library itself, not by this server's code) | all providers, indirectly | Effectively required for OAuth to work at all | If empty, gothic's `init()` still builds a `gorilla/sessions` cookie store with an empty key, prints a warning to stdout ("no SESSION_SECRET environment variable is set..."), and every `BeginAuthHandler`/`CompleteUserAuth` call will fail to persist/read the handshake state, breaking the whole flow. See `/home/chris/go/pkg/mod/github.com/markbates/goth@v1.64.2/gothic/gothic.go:44-52,116-118,159-161`. |
| `VIRTUAL_HOST` | n/a (drives multi-hostname registration, see §3) | Optional, defaults to `localhost` | Used to build `srv.Hostnames` (`/home/chris/Code/rel/_legacy/main.go:104-106`); OAuth providers/routes get registered once per hostname in this comma-separated list. |

Gating logic: `stringsHaveValue(uid_val, secret_val)` (call site
`/home/chris/Code/rel/_legacy/oauth.go:25`, helper defined at
`/home/chris/Code/rel/_legacy/env.go:28`) requires **both** KEY and SECRET to be non-empty; if either is missing the provider for that
hostname is skipped entirely (the `else` branch at `oauth.go:46-48` is an intentionally
commented-out warning log — silently disabled, no log line at all currently). There is no
partial/invalid-value validation (e.g. malformed key) — any non-empty string is accepted and
only fails later when the provider actually calls the real OAuth endpoint.

Additionally, OAuth callbacks only actually log the user in if:
- `srv.HasPg()` is true (`oauth.go:121`) — no Postgres pool, no login attempt at all, the
  callback just returns after `gothic.CompleteUserAuth` with no response written for the
  Postgres branch.
- `srv.AvailableLoginFunctions.ExternalLogin` is true (`auth.go:43-46`), which is populated by
  `ReloadLoginFunctions` (`auth.go:109-123`) by introspecting Postgres for a function
  `auth.external_login(text) returns text` (see `SQL_CHECK_FUNCTIONS`, `auth.go:88-107`). If
  that DB function doesn't exist, every external/SSO login (OAuth or SAML) is rejected with a
  403 regardless of provider config.

## 3. Providers wired up in code

`SetupOAuth` loops over `srv.Hostnames` (`oauth.go:51`, populated from `VIRTUAL_HOST`,
comma-separated) and, **for each hostname**, attempts to register all three providers
(`oauth.go:52-62`):

| Provider | Constructor | Scopes requested | Notes |
|---|---|---|---|
| `google` | `google.New(uid, secret, cbk)` (`oauth.go:52-54`) — stock goth provider, `github.com/markbates/goth/providers/google` | none passed → goth's `google` package defaults to `[]string{"email"}` (`google.go` `newConfig`, goth v1.64.2) | Fetches profile from `https://www.googleapis.com/oauth2/v2/userinfo` |
| `salesforce` | `salesforce.New(uid, secret, cbk)` (`oauth.go:55-57`) — stock goth provider, `github.com/markbates/goth/providers/salesforce` | none passed → goth's `salesforce` package leaves scopes **empty** (no default, unlike google) | Auth/token endpoints hardcoded to `https://login.salesforce.com/services/oauth2/{authorize,token}` (package-level vars, overridable only by mutating the package vars directly — the legacy server never does). User info URL is *not* a fixed endpoint: it's derived from the `id` field returned in the token response (Salesforce "identity URL"), see goth salesforce.go `FetchUser`. |
| `yahoo` | `yahoo.New(uid, secret, cbk, "email")` (`oauth.go:58-61`) — **custom in-repo provider**, `sales-way.com/server/oauth/yahoo`, not goth's built-in `providers/yahoo` | explicit `"email"` scope | See §7 for why this is a bespoke implementation. |
| `orange` | commented out (`oauth.go:64-81`) | n/a | Dead/aspirational code for an OpenID-Connect-based provider (Orange telecom, France). A `.well-known/openid-configuration` handler is still registered and live at `/oauth/orange/.well-known/openid-configuration` (`oauth.go:97-105`) returning a hardcoded JSON document pointing at `api.orange.com`, but nothing ever consumes it since `register("orange", ...)` is commented out. |

Each successfully-constructed provider has its name suffixed with the hostname
(`provimpl.SetName(provider + ":" + hostname)`, `oauth.go:34`) and is registered globally into
goth's in-memory provider list via `goth.UseProviders(provimpl)` (`oauth.go:38`) — goth
keeps one flat global map of providers by name, so multi-hostname deployments end up with
distinct provider entries like `google:app.example.com` and `google:staging.example.com`, not
one shared `google` provider. This is why `gothic.GetProviderName` is overridden
(`oauth.go:86-92`) to compute the lookup key as `<url-param>:<r.Host>` instead of goth's default
(query-string- or URL-based) lookup — this is the piece of custom glue that makes per-hostname
providers resolvable per incoming request.

Every registration also appends an entry to the shared `authEndpoints` slice
(`auth.go:38`, appended at `oauth.go:40-45`) of shape
`{Name: "oauth:google", DisplayName: "google", Endpoint: "https://<host>/oauth/google/login", Hostname: <host>}`.
This list is exposed (filtered by current request `Host`) at `GET /auth/endpoints`
(`auth.go:194-207`) — the mechanism a frontend uses to discover which login buttons to show for
the hostname it's on. SAML registers into the same list (`saml.go:248-254`).

## 4. HTTP routes registered

| Method | Path | Handler | Behavior |
|---|---|---|---|
| GET | `/oauth/orange/.well-known/openid-configuration` | inline closure, `oauth.go:97-105` | Static JSON with Orange's authorization/token/userinfo/issuer URLs. Always registered, dead code (no provider consumes it). |
| GET | `/oauth/{provider}/login` | `gothic.BeginAuthHandler` directly (`oauth.go:107`) | Resolves provider via the overridden `gothic.GetProviderName` (`{provider}:{Host}`), calls `provider.BeginAuth(state)`, persists the resulting `goth.Session` (gzip'd) into a gorilla-sessions cookie (`_gothic_session`), then HTTP 307-redirects the browser to the provider's real authorization URL. |
| GET | `/oauth/{provider}/callback` | inline closure, `oauth.go:109-124` | Calls `gothic.CompleteUserAuth(rw, req)`. On success, logs `user.UserID`/`user.Email`/provider (`oauth.go:118`), and — only if `srv.HasPg()` — calls `doExternalLogin(srv, rw, req, user.Email)` (`oauth.go:122`). |

Note the route registers only `GET` for both login and callback (goth's convention is that
providers redirect back via `GET` with `?code=...&state=...` query params — standard OAuth2
Authorization Code flow). There is no explicit method restriction; chi's `Get` only mounts the
GET verb so other methods 405 automatically via chi's router.

## 5. Full login flow, end to end

1. Frontend renders a link from `/auth/endpoints` pointing at
   `https://<host>/oauth/<provider>/login`.
2. Browser GETs that URL → `gothic.BeginAuthHandler` → `gothic.GetAuthURL` →
   `goth.GetProvider("<provider>:<host>")` → `provider.BeginAuth(state)` builds the real
   provider authorization URL (state is either taken from a `state` query param on the
   incoming request or a random 64-byte base64 nonce, see §8) → session (containing at minimum
   the `AuthURL`) is gzip-compressed and stored under session key `_gothic_session` in a
   `gorilla/sessions` cookie store keyed by `SESSION_SECRET` → 307 redirect to the provider.
3. User authenticates/consents at the provider. Provider redirects back to
   `https://<host>/oauth/<provider>/callback?code=...&state=...`.
4. `gothic.CompleteUserAuth`:
   - re-resolves the provider the same way,
   - loads the previously stored session from the `_gothic_session` cookie,
   - unmarshals it into the provider-specific `Session` type (`provider.UnmarshalSession`),
   - **validates state**: compares the `state` query param on the *original* AuthURL (parsed
     back out of the stored session) against the `state` query param on the *callback* request;
     mismatch → `"state token mismatch"` error, flow aborts (see §8),
   - calls `sess.Authorize(provider, params)` which exchanges the `code` for an access/refresh
     token via the provider's OAuth2 token endpoint,
   - re-stores the now-populated session (with tokens) back into the cookie,
   - calls `provider.FetchUser(sess)` to hit the provider's userinfo endpoint and build a
     `goth.User{UserID, Email, Name, AccessToken, RefreshToken, ExpiresAt, Provider}`,
   - **always** (`defer`) calls `gothic.Logout`, which clears/invalidates the `_gothic_session`
     cookie regardless of success or failure — the handshake cookie is single-use.
5. Legacy callback handler in `oauth.go:109-124` receives `(user, err)`.
   - On `err != nil`: writes the raw error text into the response body with `fmt.Fprintln` and
     returns — **HTTP 200** (no explicit status code set), see §6.
   - On success: logs the identity, then, only if a Postgres pool exists, calls
     `doExternalLogin(srv, rw, req, user.Email)` (`oauth.go:122`) — **note only the e-mail is
     forwarded**, `user.UserID`/`user.Name`/tokens are discarded and never reach the database.
6. `doExternalLogin` (`auth.go:41-75`):
   - 403s immediately if `ExternalLogin` capability isn't available (§2).
   - Acquires a pooled Postgres connection and runs `SELECT auth.external_login($1)` with the
     e-mail as the single argument (`auth.go:55`) — this is the actual user-identity-to-role
     mapping/lookup/upsert point; it lives entirely in the database (function contract, not
     server code). The header comment at the top of `auth.go:3-9` documents an *intended*
     3-argument signature `ext_login(uid text, email text, data json) returns text`, but the
     function actually probed/called everywhere is `auth.external_login(text) returns text`
     (`SQL_CHECK_FUNCTIONS` at `auth.go:88-91`, call site at `auth.go:55`) — the doc comment is
     stale (see §9).
   - If the function returns SQL NULL or errors, replies 401 "User not found" and logs a
     failure line; there is no auto-provisioning of new users — the mapping from OAuth identity
     to a Postgres role must already exist in the `auth` schema.
   - On success, calls `jwtSetCookieForRole(srv, req, rw, role, /*impersonator=*/false)`
     (`auth.go:69`, defined `jwt.go:253-291`) — this is the exact same function password login
     uses (`auth.go:265`) and SAML login uses (`saml.go:286`→`auth.go:41`). It optionally calls
     `auth.session_check(null, role, headers)` in Postgres to obtain a session id (if
     `AvailableLoginFunctions.SessionCheck`), mints a JWT via `jwtCreateToken` embedding at least
     `role`, `impersonator`, and the session id, and sets it as an `HttpOnly`, `Secure`,
     `SameSite=Lax` cookie named by `JWT_COOKIE` (default `accesstoken`, `env.go:37`).
   - Finally responds with `Location: https://<req.Host>` and HTTP 307 — client-side redirect
     back to the app's home page, now authenticated via cookie.

## 6. Error handling

- **Missing config**: provider not registered at all; hitting `/oauth/<provider>/login` for an
  unconfigured provider yields `goth.GetProvider` returning `ErrProviderNotFound`-style error,
  surfaced by `gothic.BeginAuthHandler` as HTTP 400 with the error text in the body.
- **Provider error / user denies consent**: the provider's redirect typically includes an
  `error`/`error_description` query param instead of `code`; `sess.Authorize` (token exchange)
  fails, `CompleteUserAuth` returns an error, and the legacy handler just does
  `fmt.Fprintln(rw, err)` with **no explicit status code** — the response is a 200 OK containing
  an error message as plain text (`oauth.go:112-116`). This is inconsistent with the rest of the
  app's `ReplyError(status, ...)` convention used elsewhere (e.g. `auth.go:44`, `auth.go:64`).
- **CSRF/state mismatch**: `gothic.CompleteUserAuth`→`validateState` returns
  `"state token mismatch"`; surfaces the same way as any other error (200 + plain text, per
  above).
- **Unknown/unmapped user** (valid OAuth identity, but no matching Postgres role): handled
  entirely inside `doExternalLogin` — 401 "User not found" (`auth.go:60-66`), only reached if
  `srv.HasPg()` was true; if there's no Postgres pool at all the callback silently no-ops (no
  error, no redirect, blank 200 response).
- **`ExternalLogin` capability disabled** (DB missing `auth.external_login`): 403 "cannot use
  external login" (`auth.go:44`), same path for OAuth and SAML.
- **Cookie write failure** (`jwtSetCookieForRole` erroring, e.g. `session_check` DB error): 500
  "internal server error" (`auth.go:70`).

## 7. The custom Yahoo package

Files: `/home/chris/Code/rel/_legacy/oauth/yahoo/yahoo.go`,
`/home/chris/Code/rel/_legacy/oauth/yahoo/session.go`.

The package header comment states its intent plainly: *"This package can be used as a reference
implementation of an OAuth2 provider for Goth."* (`yahoo.go:2`) — i.e. it's essentially a
copy/vendor of (a version of) goth's own `providers/yahoo`, kept in-repo rather than imported.
Structurally it is a near-mirror of goth's stock `salesforce`/other OAuth2-based providers (same
`Provider`/`Session` shape, same `Client()`/`Debug()`/`SetName()` boilerplate).

Concrete differences from a hypothetical "just import goth's yahoo provider":
- Hardcodes Yahoo's **OpenID Connect** endpoints directly (`yahoo.go:16-20`):
  `authURL = https://api.login.yahoo.com/oauth2/request_auth`,
  `tokenURL = https://api.login.yahoo.com/oauth2/get_token`,
  `endpointProfile = https://api.login.yahoo.com/openid/v1/userinfo` — the OIDC userinfo
  endpoint rather than a legacy Yahoo Social API endpoint.
- `userFromReader` (`yahoo.go:127-147`) parses `{sub, name, email}` — an OIDC-style claims
  payload — and maps `sub` → `goth.User.UserID`, explicitly comments **"email is not provided by
  yahoo"** (`yahoo.go:140`) even though it assigns `u.Email` there — i.e. the comment flags that
  in practice Yahoo's userinfo response for this scope configuration often omits/leaves email
  blank unless the `email` scope was granted (which the legacy server does pass, `oauth.go:59`).
- Implements `RefreshTokenAvailable() bool { return true }` and a working `RefreshToken`
  (`yahoo.go:149-163`) using `oauth2.Config.TokenSource`, so token refresh is explicitly
  supported/plumbed — this appears to be more complete than the era of goth's built-in yahoo
  provider it was presumably copied from/for (some older non-OIDC Yahoo goth providers didn't
  support refresh).
- `Session` (`session.go`) stores `AuthURL`, `AccessToken`, `RefreshToken`, `ExpiresAt` and
  marshals to/from JSON for storage inside the gzip'd gothic session cookie — identical pattern
  to every other goth OAuth2 provider's session type, nothing Yahoo-specific here.

Oddity: the package's own tests (`yahoo_test.go`, `session_test.go`) import
`github.com/markbates/goth/providers/yahoo` — goth's **upstream** yahoo package — and read
`YAHOO_KEY`/`YAHOO_SECRET` env vars (no `SW_` prefix), not the local
`sales-way.com/server/oauth/yahoo` package at all. These tests do not exercise the local code;
they look like an unmodified copy of goth's own provider test suite left in place after the
package was vendored/forked, so they provide no actual coverage of the bespoke implementation.

## 8. State / CSRF protection

Handled entirely by goth/gothic, not by legacy-server code:
- `gothic.SetState` (`gothic.go:79-96`) — if the initial `/oauth/{provider}/login` request has a
  `state` query param, that's reused verbatim (allowing a caller to supply their own state);
  otherwise a random 64-byte value from `crypto/rand`, base64 (URL-safe) encoded, is generated
  per authentication attempt.
- That state is embedded as a query param in the provider's authorization URL (via
  `oauth2.Config.AuthCodeURL(state)` inside each provider's `BeginAuth`), and the whole session
  (including the AuthURL, hence the state) is stored gzip-compressed inside a
  `gorilla/sessions` cookie-store cookie named `_gothic_session`
  (`StoreInSession`/`GetFromSession`, `gothic.go:307-328`), encrypted/signed using
  `SESSION_SECRET` as the cookie-store key (`gothic.go:44-52`).
- On callback, `validateState` (`gothic.go:218-236`) re-parses the *stored* AuthURL to recover
  the original `state` value and compares it against the `state` query param sent back by the
  provider on the callback request; any mismatch (and the stored one being non-empty) aborts
  with `"state token mismatch"`.
- This is a standard OAuth2 state-nonce CSRF defense, but its integrity depends entirely on
  `SESSION_SECRET` being set to a real secret (see §2) — with an empty key the cookie store still
  functions (an all-zero key is still usable by `securecookie`), it just isn't a real secret,
  meaning the state cookie could be forged/tampered with by anyone who knows the code is running
  with an unset `SESSION_SECRET`.
- After a successful (or failed) `CompleteUserAuth`, `gothic.Logout` is deferred and always runs,
  invalidating the `_gothic_session` cookie — so each state/nonce is single-use per attempt.

## 9. Notes / things to reconsider for the rewrite

- **Silent misconfiguration**: a provider with only one of KEY/SECRET set, or both empty, is
  silently skipped with no log output at all (the warning log line is commented out,
  `oauth.go:47`). Worth at least a startup log so misconfiguration is visible.
- **`SESSION_SECRET` is not namespaced/documented**: every other config knob in this project uses
  the `SW_` prefix (see `env.go`), but the OAuth handshake's security depends on the *unprefixed*
  `SESSION_SECRET` env var consumed by a third-party library's `init()`. It's not mentioned in
  the French README's OAuth section at all. Easy to deploy with it unset and only notice via a
  stdout print from goth.
- **Per-hostname provider fan-out**: registering a full provider instance (and OAuth app
  credentials) per virtual host (`oauth.go:51`) means N hostnames × M providers registered
  globally in goth's shared map, disambiguated only by the `name:host` string convention plus the
  overridden `GetProviderName` (`oauth.go:86-92`). This is a fairly fragile pattern (relies on
  string concatenation matching on both sides) if the rewrite needs true multi-tenant OAuth.
- **Stale doc comment**: `auth.go:3-9` documents `ext_login(uid text, email text, data json)
  returns text` but the code path actually requires/calls `auth.external_login(text) returns
  text` (single arg, no `uid`/`data`). Whichever is intended, only e-mail ever reaches the DB
  function from the OAuth path — `user.UserID`, `user.Name`, and all tokens from the provider are
  discarded after logging (`oauth.go:118-122`).
- **No user auto-provisioning**: OAuth (and SAML) login can only succeed for identities that
  already have a mapping row/logic in the `auth` schema; there's no "create account on first
  OAuth login" flow. Worth deciding deliberately for the rewrite rather than by omission.
- **Inconsistent error responses**: the OAuth callback's own error branch
  (`oauth.go:113-116`) returns HTTP 200 with the raw Go error text as the body, instead of using
  the `ReplyError` helper used everywhere else in auth.go/saml.go. Same for any state-mismatch or
  denied-consent case, since those all flow through the same `err != nil` branch.
- **Yahoo provider is a fork with a misleading test suite**: the in-repo yahoo package's tests
  exercise goth's *upstream* yahoo provider, not the local fork — zero real test coverage for the
  bespoke Yahoo code that's actually wired up in production (`oauth.go:58-61`).
- **Dead code kept live**: the Orange OIDC `.well-known` endpoint (`oauth.go:97-105`) is always
  registered and reachable even though the provider registration itself is commented out —
  harmless but pure clutter; also the commented-out `openidConnect` provider block
  (`oauth.go:71-81`) suggests OIDC (generic, not just Orange) was a planned-but-abandoned
  extension point worth deciding on explicitly in the rewrite (e.g. supporting arbitrary OIDC
  issuers instead of one bespoke provider per vendor).
- **No PKCE**: none of the three active providers configure PKCE (`code_verifier`/
  `code_challenge`); security relies solely on client secret + state nonce, consistent with
  goth v1.64.2's general lack of first-class PKCE support at the time this was written.
- **`goth.UseProviders` is a global mutable registry**: there's no way to unregister/reload a
  provider at runtime short of restarting the process; relevant if the rewrite wants dynamic
  hostname/tenant configuration without a restart.
- **Discovery endpoint conflates OAuth and SAML**: `/auth/endpoints` (`auth.go:194-207`) merges
  OAuth and SAML entries into one list distinguished only by a `Name` prefix (`oauth:` vs
  whatever SAML uses) and filtered by exact `Hostname` string match (or empty-string
  wildcard) — worth a cleaner typed model in the rewrite.
