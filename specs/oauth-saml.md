# OIDC & SAML

This document specifies the `/auth/*` endpoints that drive OpenID Connect and SAML
authentication — the redirect-driven, protocol-mandated callback URLs that can't be
modeled as an ordinary `/route/{schema}/{function}` call (`route.md`'s own dispatch
mechanism assumes a request/response the caller shapes; a browser following an IdP
redirect doesn't). Session minting once a user is authenticated — the JWT cookie, its
claims, its lifecycle — is `authentication.md`'s job, not this document's; both OIDC and
SAML end by producing an ordinary `RelHttpResponse`, at which point everything
`authentication.md ## Lifecycle` already specifies applies unchanged.

## Design

This is the same division of responsibility `authentication.md ## Session invalidation`'s
`check_session` and username/password login already use: protocol/transport mechanics in
Go, identity decisions in the database.

## Endpoints

* `POST /auth/saml/{name}/acs` — Since nothing distinguishes an IdP-initiated POST from
  the response to an SP-initiated one at this endpoint, IdP-initiated login works with no
  separate configuration.
* `GET /auth/saml/{name}/metadata` — Always served once `saml.<name>` is configured,
  independent of whether `saml.<name>.idp_metadata_url` has ever resolved — see
  `## Certificate` below for why this independence matters.

A request to any `{name}` not present in configuration is a plain `404`.

## Configuration — HTTP

* `openid.<name>.public_host` / `saml.<name>.public_host` (default empty, meaning "use
  `http.public_host`") — per-entry override of `http.public_host`, for a deployment
  reachable at more than one domain. Each named entry still answers to exactly one host —
  matching OIDC/SAML's own requirement of one exact, pre-registered redirect/ACS URL per
  registration — so serving the same logical IdP connection from two domains is two named
  entries (e.g. `openid.google_www` and `openid.google_apex`), each with its own
  `public_host`, optionally sharing the same `client_id`/`client_secret` if the IdP
  registration allows more than one redirect URI on one client.

An `openid.<name>`/`saml.<name>` entry that resolves to no effective host at all (neither
its own `public_host` nor `http.public_host` is set) is skipped — logged, not fatal to any
other configured entry — the same non-blocking treatment `## Certificate`'s
bring-your-own-cert case and `## Metadata fetch is lazy` already give a not-yet-working
endpoint.

## Configuration — OIDC

* `openid.<name>.issuer` (required) — `/.well-known/openid-configuration` is fetched from
  it for discovery.
* `openid.<name>.client_id`, `openid.<name>.client_secret` (default
  `/secrets/openid-<name>.id:./openid-<name>.id` and
  `/secrets/openid-<name>.secret:./openid-<name>.secret` — flat filenames, same
  `/secrets/`-first-then-cwd search list as `jwt.secret`/`saml.certificate_path`, with
  `<name>` substituted for the configured entry). No `$GEN$` : unlike a JWT secret or the
  SAML SP certificate, a client id/secret is issued by the IdP when the app is registered
  there — rel has no business fabricating one. Missing from config AND absent at both
  candidate paths is the same "required value missing" startup error either way ; this
  default just means the common case (create the named endpoint, drop the two files IdP
  registration gave you, no config file changes) needs no explicit
  `client_id`/`client_secret` lines at all.
* `openid.<name>.fetch_userinfo` (default `false`) — when true, its claims are merged
  over the ID token's own (`## Callback payload shape` below documents the merge precedence). Off
  by default since it's an extra round trip and most issuers already put what a typical
  deployment needs directly in the ID token.

## Configuration — SAML

* `saml.certificate_path`, `saml.private_key_path` (default
  `/secrets/saml-cert.pem:./saml-cert.pem` and `/secrets/saml-cert.key:./saml-cert.key` —
  flat filenames directly under `/secrets/`, plus a local-dev fallback, same
  colon-separated candidate search list `configuration.md ## $FILE$ value indirection`
  describes). See `## Certificate`.

## Certificate

SAML requires this deployment to act as a Service Provider with a stable identity: an
entity ID and a certificate the IdP is told to trust ahead of time. On startup, rel:

1. Tries to load `saml.certificate_path`/`saml.private_key_path`. Found: use them
   unchanged — this is the bring-your-own-certificate path, for a deployment that already
   has one issued through other means.
2. Not found: generate a self-signed keypair and certificate, write it to
   `saml.certificate_path`/`saml.private_key_path` (the first writable candidate, if
   either is a search list), and log prominently at `WARN` — filenames included — that a
   certificate was generated and needs to be handed to every configured IdP's
   administrator via `/auth/saml/{name}/metadata`.
3. On every later boot, step 1 finds the persisted file and reuses it silently (no
   warning) — the certificate must stay stable across restarts, since an IdP has been
   configured to trust that specific certificate and swapping it invalidates that trust
   with no obvious error surfaced anywhere on the SAML side.

`## Endpoints`' independence between `/auth/saml/{name}/metadata` and
`saml.<name>.idp_metadata_url` resolving exists specifically so a developer can complete
step 2 above and hand their metadata to an IdP administrator *before* that administrator
has configured anything — without that, standing up a new SAML connection is a deadlock:
neither side's metadata is meaningful until the other side already trusts it.

## Metadata fetch is lazy

A `{name}` marked not-ready re-attempts the same fetch inline, synchronously, the next
time `/auth/{oidc,saml}/{name}/login` is requested — no background retry loop, no fixed
backoff interval. Success clears the not-ready mark permanently (the endpoint behaves as
if the startup fetch had succeeded, no further retries) ; failure responds `503` to that
request and leaves the mark in place for the next attempt. Latency on that one blocked
request is an accepted cost of self-healing without a restart.

## Errors

Two distinct error sources exist on these endpoints, using the two separate code spaces
`error-handling.md ## Error codes` already defines :

* **Protocol-level failures Go itself detects** — discovery/IdP metadata not ready yet, a
  missing/mismatched OAuth2 state or nonce, a failed token exchange, an invalid ID token,
  an unparseable SAML response — never reach Postgres at all, and get their own
  `SSO_*` rel-internal codes (`error-handling.md ## Rel-internal codes`), not the
  callback function's `RSxxx` family.
* **The callback function's own rejection** — `## Callback function`'s `raise exception
  ... using errcode = 'RSxxx'` convention, unchanged, once a request has actually reached
  the database.

A client distinguishes the two the same way it already distinguishes any other
`RSxxx`/rel-internal pair : by the `code` field/`X-Rel-Errorcode` header
(`error-handling.md ## Delivery`), not by inspecting HTTP status alone — `SSO_NOT_READY`
and a callback-rejected login can both plausibly render as a 4xx/5xx status a client-side
switch still needs to tell apart.

## Callback payload shape

`sso.ssoCallbackPayload` (`sso/claims.go`), passed as the callback function's `payload`
argument — the user-facing version of this is
`docs/content/http/authentication.md ## OpenID Connect and SAML`'s `SsoCallback` :

```typescript
type SsoCallbackPayload = {
  jwt: JWT | null,
  identity: SsoIdentity,
  state: unknown,
}

type SsoIdentity = {
  protocol: "oidc" | "saml",
  name: string,          // the configured openid.<name>/saml.<name> entry
  // OIDC: the ID token's claims, with the userinfo endpoint's claims merged
  // over them (userinfo wins on key collision) when fetch_userinfo is true.
  // SAML: every assertion attribute, keyed by its attribute name.
  claims: { [key: string]: unknown },
  // OIDC only : present whenever the token exchange returned one,
  // regardless of fetch_userinfo. SAML has no equivalent — never present.
  access_token?: string,
  refresh_token?: string,
}
```

`identity` is nested, not flattened into `payload` directly — `payload.claims.claims` would
be a stutter, and this keeps `claims` meaning exactly one thing (the protocol-supplied
identity claims) at every nesting depth.

A SAML attribute's value is always a string array (`string[]`), even for a
single-valued attribute — SAML attributes are multi-valued by the spec, and rel does not
guess at collapsing single-element arrays, leaving that decision to the callback
function. OIDC claim values keep whatever JSON shape the ID token/userinfo response gave
them (string, number, boolean, array, nested object).

`identity.access_token`/`identity.refresh_token` are included so the callback function (or,
via a custom claim it chooses to embed into the minted JWT, the application itself later) can
call back into the IdP's own API on the user's behalf — rel neither stores nor manages either
token beyond handing them to this one call.

`jwt` is the browser's OWN current session (the same shape `RelHttpRequest.jwt` uses),
`null` if it has none — read directly off the callback request via `jwt.VerifyRequest`
(the exact function every other authenticated request's session is verified through), NEVER
threaded through the IdP. Reliably populated for OIDC's `GET` callback under
`jwt.same_site=Lax` (the default) — a cross-site top-level GET navigation still sends a Lax
cookie — but NOT for SAML's `POST /acs` under the same default, since Lax cookies are not
sent on a cross-site POST ; a deployment needs `jwt.same_site=None` for this to populate on
SAML callbacks too.

`state` is whatever `## Passing state through login` decoded `/login`'s own query string
into, round-tripped unchanged.

## Callback function

Takes plain `jsonb`, not the `RelHttpRequest` domain — same reasoning as
`check_session`'s signature (`authentication.md ## Session invalidation`): this keeps the
function out of `route.md`'s route auto-discovery regardless of its name, since it isn't
meant to be callable directly at `/route/...`. Returns `RelHttpResponse` (the configured
response domain, `route.md ## Configuration`'s `http.response_domain_name`), so setting
extra cookies or redirecting the browser onward after login reuse `route.md ## Responses`
unchanged — nothing SSO-specific about the response shape.

No function configured, and no `http.functions.sso_callback` fallback either, is a
startup-time `WARN` (same non-fatal "won't work until configured" treatment
`route.md ## Configuration` gives an unresolved `http.response_domain_name`) — the
endpoint's `/login` route still exists, redirects still work, but its `/callback`/`/acs`
always `500`s until a function is configured.

## Passing state through login

`/login`'s own raw query string (`r.URL.RawQuery`) is decoded via
`querystring.DecodeQueryField` — the same structural layer `RelHttpRequest.query` already
uses (`route/encode.go`) — nil for an empty query string. `sso/state.go` carries the
decoded value through whichever mechanism the protocol provides :

- **OIDC** : composed alongside rel's own CSRF token in the outgoing `state` URL param,
  `csrfToken + "." + base64url(json(state))` (`encodeStateForTransit`). Base64url specifically
  because its alphabet excludes `.`, which is what lets the callback split the two apart
  unambiguously (`strings.Cut` on the first `.`) — only the `csrfToken` half is ever
  validated ; the state half decodes back to the original value regardless of what it
  contains. `""` (nil state) leaves the outgoing param exactly as it was before this
  feature existed — no compositing, no trailing `.`.
- **SAML** : plain JSON, no base64, carried directly as RelayState
  (`encodeRelayState`/`decodeRelayState`) — RelayState shares its slot with nothing else
  (SAML's CSRF/replay protection comes from the signed assertion and `InResponseTo`, never
  from RelayState), so no compositing is needed. A RelayState that fails to decode as JSON
  (an IdP-initiated login never went through `/login` at all, so has none to begin with ; some
  IdPs may echo back their own convention) degrades to `state: null` rather than rejecting
  the login — absence of this feature's data is not an attack.

> Why OIDC needs the `.`-split scheme and SAML doesn't : OIDC's `state` param IS rel's CSRF
> token (compared byte-for-byte against the cookie on return) ; letting the app supply the
> whole value would make CSRF protection only as strong as whatever unguessability the app's
> own value happens to have. RelayState was never used for anything security-relevant to begin
> with (hardcoded `""` before this feature existed), so it's free for the app's value alone.

The decoded value is never validated or interpreted by rel itself beyond that round trip —
see `docs/content/http/authentication.md ## Passing state through login`'s security note
(attacker-influenceable, IdP-visible, open-redirect risk if used to build a redirect without
validation) for what a callback function needs to keep in mind before acting on it.

## Example

Purely illustrative — not a shipped default, not the `http.functions.sso_callback`
fallback discussed above, just a sketch of the kind of function a developer would write.
Assumes a pre-existing `users(username text, role text)` table and tries a fixed list of
commonly-seen claim/attribute names from both protocols — email-like ones first, since
those tend to be the most consistently populated across IdPs, then username-like ones —
logging the first user in on whichever key first resolves to an existing row:

```sql
create function auth.example_sso_login(payload jsonb) returns RelHttpResponse
language plpgsql
security definer
as $$
declare
  candidate_keys text[] := array[
    -- Email-like claims/attributes, tried first.
    'email', 'mail', 'emailaddress', 'emails',
    'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress',
    'urn:oid:0.9.2342.19200300.100.1.3', -- LDAP "mail"
    -- Username-like claims/attributes, tried next.
    'preferred_username', 'username', 'user', 'nickname', 'upn',
    'unique_name', 'uid', 'samaccountname', 'sub', 'NameID',
    'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name',
    'urn:oid:0.9.2342.19200300.100.1.1' -- LDAP "uid"
  ];
  key text;
  raw jsonb;
  candidate text;
  matched_role text;
begin
  foreach key in array candidate_keys loop
    raw := payload -> 'identity' -> 'claims' -> key;
    if raw is null then
      continue;
    end if;

    -- SAML attribute values are always string[] (## Callback payload shape) ;
    -- OIDC claims are scalars. Take the first element either way.
    candidate := case jsonb_typeof(raw)
      when 'array' then raw ->> 0
      else raw #>> '{}'
    end;

    if candidate is null then
      continue;
    end if;

    -- pretend "users" table that has at least (role text, username text)
    select role into matched_role from users where username = candidate limit 1;
    if matched_role is not null then
      return jsonb_build_object('jwt', jsonb_build_object('role', matched_role));
    end if;
  end loop;

  raise exception 'No account matches any known identity claim' using errcode = 'RS401';
end;
$$;
```
