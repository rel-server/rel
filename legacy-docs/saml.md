# SAML Subsystem (legacy `sales-way.com/server`)

Source: `_legacy/saml.go` (327 lines), wired from `_legacy/main.go:196`. Built entirely on
`github.com/crewjam/saml v0.4.14` (`_legacy/go.mod:10`) — specifically the `saml` (core SP/IdP
protocol types, `saml.ServiceProvider`, `saml.InvalidResponseError`, package vars
`MaxIssueDelay`/`MaxClockSkew`) and `samlsp` (`samlsp.Middleware`, `samlsp.New`/`samlsp.Options`,
`samlsp.FetchMetadata`, `samlsp.AttributeFromContext`, `samlsp.SessionFromContext`,
`samlsp.JWTSessionClaims`, `samlsp.SessionWithAttributes`) sub-packages. Routing glue uses
`github.com/go-chi/chi`. Debug attribute dumping uses `github.com/k0kubun/pp`.

## 1. Purpose / role

`setupSaml` (`_legacy/saml.go:137`) turns the app into a SAML Service Provider (SP) for zero or
more externally configured Identity Providers (IdPs), one config block per virtual hostname the
server answers to. A successful SAML login is funneled through the exact same
"external login" path used by OAuth (`doExternalLogin`, `_legacy/auth.go:41`): the SAML NameID/
attribute value is handed to a single Postgres function, `auth.external_login(text)`, which
decides whether the user exists and which Postgres role to log them in as. SAML itself never
touches roles/permissions directly — it only produces a "username" string.

## 2. Environment variables

| Var | Read at | Format | Required | Default |
|---|---|---|---|---|
| `SW_SAML_IDP` | `_legacy/env.go:42` (`SAML_IDP = getenvOrDefault("SW_SAML_IDP", "")`) | `name:metadata_url` pairs separated by `;` for multiple IdPs, e.g. `samltest:https://samltest.id/saml/idp` or `samltest:https://samltest.id/saml/idp; tkd:https://okta.com/saml/takeda/idp` (README.md:110-113). Each pair is split on the **first** `:` only (`strings.SplitN(idp_str, ":", 2)`, `saml.go:182`), so the metadata URL itself may contain colons (e.g. `https://`). If the provider name segment starts with `%`, that `%` is stripped and `force_signed = true` is set for that IdP only (`saml.go:190-195`) — **undocumented in README**, only discoverable in code. | Optional overall — if empty, SAML is not enabled at all (see below). | `""` |
| `VIRTUAL_HOST` | `_legacy/main.go:104` | comma-separated hostnames | Not SAML-specific, but SAML setup loops over `srv.Hostnames` (split from this var) to register a middleware instance per (IdP, hostname) pair. | `localhost` (with a warning logged) |

No dedicated `SW_SAML_*` variable exists for certificates, private keys, entity ID, ACS URL,
force-authn, clock skew, or attribute names — none of those are configurable via environment;
they are either hardcoded or derived from other settings (see §3, §8).

If `SW_SAML_IDP` is unset/empty, `setupSaml` returns
`fmt.Errorf("saml: no $SW_SAML_IDP variable, not enabling SAML")` (`saml.go:150`). The caller in
`main.go:196-198` only logs this as an error and continues booting — SAML is silently absent, not
a fatal startup condition.

## 3. Service Provider certificate / key material

There is exactly **one** SP keypair, shared across every configured IdP and hostname (loaded
once, before the per-IdP loop, `saml.go:140-176`):

1. On startup, try to load an existing keypair from **hardcoded** paths
   `/app/saml/cert.pem` / `/app/saml/cert.key` via `tls.LoadX509KeyPair` (`saml.go:158`). If that
   succeeds, log `"saml: reusing previously generated certificate"` and reuse it.
2. If loading fails (first boot, volume wiped, etc.), call
   `generateSelfSignedCertificate(srv.Hostnames)` (`saml.go:60-134`):
   - RSA-2048 key (`rsa.GenerateKey(rand.Reader, 2048)`, `saml.go:62`).
   - Self-signed x509 cert, `Subject.Organization = ["Acme Co"]` (hardcoded leftover from the Go
     stdlib example, `saml.go:69`), `NotBefore = now`, `NotAfter = now + 2 years` (`saml.go:71-72`),
     `KeyUsage = KeyEncipherment|DigitalSignature`, `ExtKeyUsage = ServerAuth`,
     `DNSNames = srv.Hostnames` (`saml.go:80`).
   - Writes the resulting cert/key back to the same hardcoded `/app/saml/cert.pem` /
     `/app/saml/cert.key` paths (`saml.go:114-131`) so subsequent restarts reuse it (step 1).
   - There is no directory-creation step; if `/app/saml` does not exist the `os.Create` calls
     fail and the whole function returns an error, aborting SAML setup entirely.
3. The leaf certificate is parsed once more (`x509.ParseCertificate`, `saml.go:174`) and the
   private key is cast to `*rsa.PrivateKey` when building `samlsp.Options.Key`
   (`saml.go:222`) — this cast will panic if a non-RSA key were ever present, though in practice
   only RSA keys are ever generated.

No IdP metadata/cert is read from local files or env — each IdP's metadata (including its signing
certificate) is fetched live over HTTP at startup (see §4).

## 4. HTTP routes registered per configured IdP

For every `name:metadata_url` entry in `SW_SAML_IDP`, and for every hostname in `srv.Hostnames`,
the code builds a `*samlsp.Middleware` via `samlsp.New` with `URL` set to
`https://<hostname>/saml/<name>/` (`saml.go:202`, `saml.go:220-230`). `samlsp` derives sub-paths
from that root URL (`samlsp/new.go:94-96`):

- Metadata: `GET /saml/<name>/saml/metadata` — served by `Middleware.ServeMetadata`
  (`crewjam/saml samlsp/middleware.go:55-56,69-76`), content-type
  `application/samlmetadata+xml`.
- ACS (assertion consumer): `/saml/<name>/saml/acs` — served by `Middleware.ServeACS`
  (`middleware.go:60-61,79-103`). This is where the IdP's browser-POSTed `SAMLResponse` lands.
- SLO: `samlsp` also computes an `SloURL` of `/saml/<name>/saml/slo` internally
  (`samlsp/new.go:96,123`), and this path is included in the SP metadata that's advertised to the
  IdP, **but** `Middleware.ServeHTTP` only dispatches on `MetadataURL.Path` and `AcsURL.Path`
  (`middleware.go:54-66`) — hitting `/saml/<name>/saml/slo` falls through to
  `http.NotFoundHandler`. SLO is effectively advertised but non-functional (see §9).
- Login initiation: `/saml/<name>/saml/login` — this route is **app-defined**, not part of
  `samlsp`'s automatic dispatch. It is registered explicitly (`saml.go:301-313`) as
  `middleware.RequireAccount(do_login)` (`saml.go:290`), i.e. "ensure a valid SAML session exists,
  redirecting to the IdP if not, then run `do_login`".

These are wired up per-provider (not per-hostname) as a chi sub-router
(`srv.Router.Route("/saml/"+provider+"/saml", ...)`, `saml.go:299-323`):

```
r.Handle("/login", ...)   // saml.go:301  — looks up hostnameMap[r.Host]
r.Mount("/", ...)         // saml.go:315  — delegates everything else to middleware.ServeHTTP,
                           //                also looked up by r.Host
```

Both handlers key off `r.Host` into a `hostnameMap[hostname] -> samlServe{login, middleware,
hasmetadata}` (`saml.go:183,292-296`) built during the per-hostname inner loop, so the actual
per-request dispatch is by **(provider-name-from-path, Host-header)**, giving each hostname its
own SP entity/cert-derived-URL/session cookie scope even though they share the same underlying
RSA keypair.

## 5. Full login flow

1. **Initiation** — client hits `GET /saml/<name>/saml/login` on one of the configured hostnames.
   The handler (`saml.go:301-313`) looks up `hostnameMap[r.Host]`:
   - Unknown host → `http.NotFoundHandler` (404).
   - Known host but `hasmetadata == false` (IdP metadata never resolved, see step 2 below) → `503`
     `"No SAML IDP"` / `"The SAML provider '<name>' is not configured or its metadata is unknown"`
     (`saml.go:304-309`).
   - Otherwise → `item.login.ServeHTTP` = `middleware.RequireAccount(do_login)`
     (crewjam `middleware.go:109-124`): checks for an existing `samlsp` session cookie (default
     name `"token"`, `samlsp/session_cookie.go:11`, 1 hour default max-age,
     `samlsp/session_jwt.go:15`). If present and valid, skips straight to `do_login`. If absent,
     calls `HandleStartAuthFlow` which builds a SAML `AuthnRequest`, stores a short-lived
     `saml_...`-prefixed tracking cookie (JWT-encoded request ID + originally requested URL,
     signed with the SP's RSA key), and redirects the browser to the IdP's SSO endpoint
     (redirect or POST binding depending on `Middleware.Binding`, default `""` → redirect
     binding).
2. **At startup** (not per-request), IdP metadata for each `metadata_url` is fetched once via
   `samlsp.FetchMetadata(ctx, http.DefaultClient, idpurl)` (`saml.go:213`). Failure is logged
   (`sw.LogError`, `saml.go:215`) and that provider is left with `idpmetadata = nil` /
   `hasmetadata = false` permanently for the life of the process — no retry loop.
3. **IdP authenticates the user** and redirects/POSTs a `SAMLResponse` back to
   `/saml/<name>/saml/acs`.
4. **ACS validation** (`Middleware.ServeACS`, `middleware.go:79-103`): parses the form, builds the
   list of acceptable `InResponseTo` request IDs from (a) `""` if
   `AllowIDPInitiated == true` (always true here, see §8) and (b) any currently tracked
   `saml_...` cookies, then calls `ServiceProvider.ParseResponse(r, possibleRequestIDs)`. This
   validates the XML signature against the IdP's metadata certificate, destination/audience,
   `IssueInstant` freshness (`saml.MaxIssueDelay`), and `Conditions.NotBefore`/`NotOnOrAfter`
   (`saml.MaxClockSkew`) — see §8 for the exact values, which are library defaults, not configured
   here.
5. On success, `CreateSessionFromAssertion` issues the `"token"` session cookie (a JWT signed with
   the SP's own RSA key, containing the assertion's attributes) and 302-redirects back to the
   originally requested URL (recovered from the tracking cookie) — normally back to
   `/saml/<name>/saml/login`.
6. **Back at `/saml/<name>/saml/login`**, `RequireAccount` now finds a valid session and invokes
   `do_login` (`saml.go:255-287`), which resolves a username in this fixed order:
   1. SAML attribute named `"username"` (`samlsp.AttributeFromContext(ctx, "username")`).
   2. SAML attribute named `"user"`.
   3. The session's JWT `Subject` claim (i.e. the assertion's NameID), via
      `samlsp.SessionFromContext` cast to `samlsp.JWTSessionClaims`.
   If none of these yield a non-empty string → `401` `"No user in response"` /
   `"Impossible to figure out who is trying to log in"` (`saml.go:274-283`); the full attribute set
   is additionally pretty-printed to server stdout via `pp.Print` (not sent to the client) for
   debugging.
7. `doExternalLogin(srv, w, r, username)` (`auth.go:41-75`):
   - If `srv.AvailableLoginFunctions.ExternalLogin` is false (i.e. Postgres has no
     `auth.external_login(text)` function, as detected at boot by `ReloadLoginFunctions`,
     `auth.go:109-123` via an `information_schema` probe) → `403` `"External login is disabled"`.
   - Otherwise runs `SELECT auth.external_login($1)` with the resolved username. A null result or
     query error → `401` `"User not found"` (or the raw DB error text — see §9).
   - A non-null result is treated as a Postgres role name and passed to
     `jwtSetCookieForRole(srv, req, rw, role, false)` (`jwt.go:253-...`), which optionally calls
     `auth.session_check(...)` for session bookkeeping, mints an app JWT (role + `impersonator:false`
     + session id) and sets it as the `JWT_COOKIE` cookie (env `SW_JWT_COOKIE`/`PGRST_JWT_COOKIE`,
     default `"accesstoken"`, `env.go:37`).
   - Responds `307 Temporary Redirect` to `https://<Host>/` (`auth.go:73-74`).

## 6. Multi-IdP support

- Each `;`-separated entry in `SW_SAML_IDP` becomes an independent provider identified purely by
  the `<name>` path segment in `/saml/<name>/saml/...` (`saml.go:181-323`). There is no shared
  state between providers other than the single SP RSA keypair.
- Within a provider, a further loop over `srv.Hostnames` creates one `samlsp.Middleware` per
  hostname (`saml.go:199-297`), each with its own root `URL` (`https://<hostname>/saml/<name>/`)
  and therefore its own derived metadata/ACS/SLO URLs and session-cookie domain — this lets the
  same IdP-name be served correctly on multiple virtual hosts.
- Attribute-to-username mapping (`do_login`, §5 step 6) is **identical for every provider and
  every hostname** — there is no per-IdP configuration of which SAML attribute carries the
  identity, no role/email attribute consumption, and no way to express it via
  `SW_SAML_IDP` beyond the name/URL/`%`-signing-flag. All authorization logic is deferred to the
  single Postgres function `auth.external_login(text)`.
- The only other per-provider knob is the `%` prefix on the name (`saml.go:190-195`), which sets
  `SignRequest: true` for that provider's `samlsp.Options`, causing outgoing `AuthnRequest`s to be
  signed (RSA-SHA1, `samlsp/new.go:102-104`) with the SP key.

## 7. Error handling

- **Unknown provider name in URL**: not present in the chi route table at all → chi's normal
  404 handling (no explicit code needed, routes are only registered for names that appear in
  `SW_SAML_IDP`).
- **Known provider, unknown/mismatched `Host` header**: explicit `http.NotFoundHandler`
  (`saml.go:311-312, 319-320`).
- **IdP metadata never resolved** (fetch failed at boot): `503` on every `.../saml/login` hit for
  that provider, forever, until process restart (`saml.go:304-309`).
- **Invalid / expired / unsigned / malformed assertion**: surfaces from
  `ServiceProvider.ParseResponse` as a `*saml.InvalidResponseError`. The custom
  `middleware.OnError` (`saml.go:232-241`) special-cases this type: responds `500` `"SAML error"`
  with the **private error detail embedded in the response body**
  (`"SAML encountered an error: "+err2.PrivateErr.Error()`) and logs it; any other error type gets
  a generic `500` with `err.Error()` as the body — both leak internal error text to the client
  (flagged in §9).
- **Assertion valid but no resolvable username**: `401` `"No user in response"` (`saml.go:274-283`).
- **External login rejected by Postgres** (`auth.external_login` returns null/errors): `401`
  `"User not found"` or the raw error message (`auth.go:59-66`).
- **External login feature disabled** (function missing in DB): `403`
  `"External login is disabled"` (`auth.go:43-46`).

## 8. Timing, clock skew, replay protection

None of this is configurable via env or code in `_legacy/saml.go` — it all comes from
`crewjam/saml` package-level defaults:

- `saml.MaxIssueDelay = 90 * time.Second` (`service_provider.go:132`) — max age of an assertion's
  `IssueInstant` relative to "now" at validation time; also reused as the max-age of the
  short-lived `saml_...` request-tracking cookie (`samlsp/new.go:73,85`).
- `saml.MaxClockSkew = 180 * time.Second` (`service_provider.go:136`) — leeway applied to
  `Conditions.NotBefore` / `NotOnOrAfter` and `SubjectConfirmationData.NotOnOrAfter` checks.
- Replay protection is cookie-based, not a server-side nonce/used-ID store: the SP tracks
  outstanding `AuthnRequest` IDs via signed `saml_...` cookies with `MaxAge = MaxIssueDelay`
  (`samlsp/new.go:78-89`) and matches them against the response's `InResponseTo`.
- Crucially, `AllowIDPInitiated: true` is **hardcoded** in every provider's `samlsp.Options`
  (`saml.go:226`, comment: `// For dev, will remove FIXME`). This makes `""` (no request ID)
  always a valid `InResponseTo` (`ServeACS`, `middleware.go:87-89`), i.e. every configured IdP can
  send unsolicited ("IdP-initiated") assertions that bypass the request-tracking/replay check
  entirely — the only remaining protections are the assertion's own `IssueInstant`/`NotOnOrAfter`
  windows and the standard "has this exact assertion been consumed before" — which is not checked
  anywhere (`ParseResponse` does not maintain an assertion-ID replay cache in this version of the
  library as used here). This is a live FIXME left by the original author, not a resolved issue.

## 9. Notes / things to reconsider for the rewrite

- **`AllowIDPInitiated: true` is hardcoded for every provider** with an explicit `// FIXME` saying
  it should be dev-only (`saml.go:226`). Combined with no assertion-replay cache, this is the
  single biggest thing to fix before treating any of this as production-grade SSO.
- **Single shared SP keypair for all IdPs/hostnames**, stored at hardcoded, non-configurable
  paths `/app/saml/cert.pem` / `/app/saml/cert.key` (`saml.go:115,124,158`). No rotation: the cert
  is valid for a fixed 2 years (`saml.go:72`) with no renewal logic — on expiry, SAML silently
  starts failing until someone deletes those files to force regeneration. No path/volume is
  configurable via env.
- **Self-signed cert `Subject.Organization` is literally `"Acme Co"`** (`saml.go:69`), a copy-paste
  leftover from the Go standard library's certificate-generation example that was never
  customized.
- **SP metadata advertises a Single Logout (SLO) endpoint that doesn't work.** `samlsp` computes
  and publishes an `SloURL` (`/saml/<name>/saml/slo`), but `Middleware.ServeHTTP` never routes to
  it (only Metadata + ACS paths are dispatched, `middleware.go:54-66`), and the app's own
  `/auth/logout` has an explicit unaddressed `// FIXME perform SAML logout` (`auth.go:361`). SAML
  logout is entirely unimplemented despite being advertised to IdPs.
- **Attribute-to-username mapping is fixed and global**, not configurable per IdP: attribute
  `"username"`, then `"user"`, then JWT `Subject`/NameID, in that order, for every provider
  (`saml.go:257-272`). Any IdP whose assertion doesn't use one of those exact attribute names (or
  a usable NameID) cannot log a user in, and there's no per-IdP override.
- **Undocumented `%`-prefix syntax** on the provider name in `SW_SAML_IDP` to request signed
  `AuthnRequest`s (`saml.go:190-195`) — not mentioned anywhere in `README.md`; easy to miss.
- **Error responses leak internal detail to the client**: `middleware.OnError` puts
  `err2.PrivateErr.Error()` straight into the HTTP body for `saml.InvalidResponseError`
  (`saml.go:236`), and `doExternalLogin` can reflect a raw Postgres/driver error string into a 401
  body (`auth.go:60-64`). Should be sanitized for any externally-facing error path in the rewrite.
- **IdP metadata is fetched exactly once, at process startup**, with no retry/refresh (`saml.go:213`).
  A transient network blip while the app is (re)starting permanently disables that IdP (`503`)
  until the whole process is restarted again — there's no admin action to force a re-fetch.
- **`pp.Print` debug dump of full SAML attributes to server stdout** on every failed
  username-resolution attempt (`saml.go:279`) — a potential PII-in-logs concern, and dead-ish code
  (`pp.Fprint(w, ...)` is commented out right below it, `saml.go:280`).
- **Stale top-of-function TODO**: `// TODO generate an X509 certificate that is self signed.`
  (`saml.go:146`) sits directly above code that already does exactly that — leftover comment, and
  a large commented-out block (`saml.go:81-94`) about IP/CA handling that's never used.
- **Cast `keyPair.PrivateKey.(*rsa.PrivateKey)` without a type-switch/ok-check** (`saml.go:222`) —
  safe today because only RSA keys are ever generated, but fragile if that ever changes (e.g. to
  support ECDSA).
- **No SAML-specific rate limiting / brute-force protection** anywhere in this file — relies
  entirely on whatever global middleware the server applies elsewhere.
- **Provider/hostname matrix is O(providers × hostnames) middleware instances** built at startup
  (`saml.go:181,199`) with no lazy loading or reconfiguration — the README's own TODO list
  (`README.md:11`) calls out making OAuth/SAML endpoint creation "dynamique" so the server can
  reconfigure without a full restart; this file is exactly the code that TODO refers to.
