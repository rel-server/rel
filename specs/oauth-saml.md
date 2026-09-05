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

Both protocols reduce to the same shape: redirect the user to a foreign identity
provider, receive a callback carrying whatever that provider is willing to assert about
the user, and hand the entire assertion — as JSON, unfiltered, uninterpreted — to a
developer-supplied Postgres function. That function is the only place identity claims get
interpreted into a role: rel does not itself decide what an email, a `groups` claim, or a
SAML attribute means. This is the same division of responsibility `authentication.md
## Session invalidation`'s `check_session` and username/password login already use:
protocol/transport mechanics in Go, identity decisions in the database.

Each configured endpoint — an OIDC issuer or a SAML IdP — is a named entry
(`openid.<name>.*` / `saml.<name>.*`, `authentication.md`'s own naming). `<name>` is
chosen by the developer and has no relationship to a provider's brand; nothing about
either protocol's configuration is hardcoded to a specific IdP (Google, Okta, ...) the way
a per-provider library integration would be.

## Endpoints

For every configured `openid.<name>`:

* `GET /auth/oidc/{name}/login` — redirects to the issuer's authorization endpoint.
* `GET /auth/oidc/{name}/callback` — the OAuth2 redirect target ; exchanges the code,
  verifies the ID token, builds `## Claims shape` below, and calls the configured
  callback function.

For every configured `saml.<name>`:

* `GET /auth/saml/{name}/login` — SP-initiated flow: redirects to the IdP with a signed
  or unsigned `AuthnRequest` per `saml.<name>.force_signed_requests`.
* `POST /auth/saml/{name}/acs` — the Assertion Consumer Service: parses the IdP's
  response, builds `## Claims shape` below, and calls the configured callback function.
  Since nothing distinguishes an IdP-initiated POST from the response to an SP-initiated
  one at this endpoint, IdP-initiated login works with no separate configuration.
* `GET /auth/saml/{name}/metadata` — this deployment's SP metadata (entity ID, ACS URL,
  signing/encryption certificate), as XML. Always served once `saml.<name>` is
  configured, independent of whether `saml.<name>.idp_metadata_url` has ever resolved —
  see `## Certificate` below for why this independence matters.

A request to any `{name}` not present in configuration is a plain `404`.

## Configuration — HTTP

* `http.public_host` (default empty) — this deployment's own externally-reachable domain :
  a bare host (`app.example.com`), never a full URL — no scheme, port, or path. rel always
  builds `https://<host>/...` from it for OIDC's `redirect_uri` and SAML's metadata/ACS
  URLs.
  > Why a bare host, not a full URL : a full URL invites the misconfigurations a domain
  > can't have — a stray trailing slash, an accidental path segment, `http://` typo'd for
  > `https://`. The scheme specifically isn't a real choice here : the JWT cookie this
  > whole system mints is already `Secure`-only (`authentication.md`), so rel is already
  > assumed to be reached over HTTPS in any real deployment — via a TLS-terminating
  > reverse proxy/ingress in front of it, the same relationship `http.host`/`http.port`
  > (rel's own, internal bind address) already has to the outside world. rel has no native
  > HTTPS/TLS termination of its own ; that's a distinct, unbuilt feature; a real
  > certificate for a real public domain needs ACME-style issuance (port 80/443 ownership,
  > a domain challenge, ongoing renewal), which is orthogonal to OIDC/SAML specifically and
  > out of scope here.
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

* `openid.<name>.issuer` (required) — the issuer URL; `/.well-known/openid-configuration`
  is fetched from it for discovery.
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
* `openid.<name>.scopes` (default `["openid", "email", "profile"]`).
* `openid.<name>.fetch_userinfo` (default `false`) — when true, after token exchange the
  discovered userinfo endpoint is also called (with the obtained access token), and its
  claims are merged over the ID token's own (`## Claims shape` below documents the merge
  precedence). Off by default since it's an extra round trip and most issuers already put
  what a typical deployment needs directly in the ID token.
* `openid.<name>.callback_function` (default empty) — unquoted, fully qualified name of
  the Postgres function this endpoint's callback calls. Falls back to
  `http.functions.sso_callback` (`## Callback function` below) when unset.

## Configuration — SAML

* `saml.<name>.idp_metadata_url` (required) — fetched once at startup (`## Metadata
  fetch is lazy` below covers failure handling).
* `saml.<name>.force_signed_requests` (default `true`) — sign the outgoing
  `AuthnRequest` with this deployment's SP key.
* `saml.<name>.callback_function` (default empty) — same fallback rule as
  `openid.<name>.callback_function`, onto `http.functions.sso_callback`.
* `saml.certificate_path`, `saml.private_key_path` (default
  `/secrets/saml-cert.pem:./saml-cert.pem` and `/secrets/saml-cert.key:./saml-cert.key` —
  flat filenames directly under `/secrets/`, plus a local-dev fallback, same
  colon-separated candidate search list `configuration.md ## $FILE$ value indirection`
  describes) — this deployment's SP certificate/key, shared across every configured
  `saml.<name>` entry (one SP identity, potentially many IdPs). See `## Certificate`.

Both `openid.<name>.callback_function` / `saml.<name>.callback_function` and
`http.functions.sso_callback` are named after `authentication.md`'s existing
`http.functions.check_session` setting; the specific default function name below is
illustrative, not fixed — final naming should follow whatever convention
`authentication.md`'s own function-naming settles on.

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

Both `saml.<name>.idp_metadata_url` (SAML) and `openid.<name>.issuer` discovery (OIDC)
are fetched once at startup. A failure there (the IdP/issuer isn't reachable yet, or its
administrator hasn't finished configuring their side — exactly the bootstrapping order
`## Certificate` describes) does not fail startup: rel logs a `WARN` naming the endpoint
and leaves it marked not-ready.

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

## Claims shape

Both protocols converge on the same JSON object, passed as the callback function's
`claims` argument:

```typescript
type SsoClaims = {
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

A SAML attribute's value is always a string array (`string[]`), even for a
single-valued attribute — SAML attributes are multi-valued by the spec, and rel does not
guess at collapsing single-element arrays, leaving that decision to the callback
function. OIDC claim values keep whatever JSON shape the ID token/userinfo response gave
them (string, number, boolean, array, nested object).

`access_token`/`refresh_token` are included so the callback function (or, via a custom
claim it chooses to embed into the minted JWT, the application itself later) can call
back into the IdP's own API on the user's behalf — rel neither stores nor manages either
token beyond handing them to this one call.

## Callback function

```sql
create function auth.sso_callback(claims jsonb) returns "RelHttpResponse"
language plpgsql
security definer
as $$
declare
  matched_role text;
begin
  -- Inspect claims->>'protocol', claims->'claims', decide a role, mint a
  -- session exactly like any other RelHttpResponse.jwt-setting function
  -- (authentication.md ## Lifecycle, step 1).
  if matched_role is null then
    raise exception 'No account for this identity' using errcode = 'RS401';
  end if;

  return jsonb_build_object('jwt', jsonb_build_object('role', matched_role));
end;
$$;
```

Takes plain `jsonb`, not the `RelHttpRequest` domain — same reasoning as
`check_session`'s signature (`authentication.md ## Session invalidation`): this keeps the
function out of `route.md`'s route auto-discovery regardless of its name, since it isn't
meant to be callable directly at `/route/...`. Returns `RelHttpResponse` (the configured
response domain, `route.md ## Configuration`'s `http.response_domain_name`), so minting a
session, setting extra cookies, or redirecting the browser onward after login all reuse
`route.md ## Responses`/`authentication.md ## Lifecycle` unchanged — nothing SSO-specific
about the response shape.

Rejecting a login is the same convention every other function in this system uses:
`raise exception ... using errcode = 'RSxxx'` aborts with that status
(`error-handling.md`'s `RSxxx` convention) instead of minting anything.

No function configured, and no `http.functions.sso_callback` fallback either, is a
startup-time `WARN` (same non-fatal "won't work until configured" treatment
`route.md ## Configuration` gives an unresolved `http.response_domain_name`) — the
endpoint's `/login` route still exists, redirects still work, but its `/callback`/`/acs`
always `500`s until a function is configured.

## Example

Purely illustrative — not a shipped default, not the `http.functions.sso_callback`
fallback discussed above, just a sketch of the kind of function a developer would write.
Assumes a pre-existing `users(username text, role text)` table and tries a fixed list of
commonly-seen claim/attribute names from both protocols — email-like ones first, since
those tend to be the most consistently populated across IdPs, then username-like ones —
logging the first user in on whichever key first resolves to an existing row:

```sql
create function auth.example_sso_login(claims jsonb) returns RelHttpResponse
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
    raw := claims -> 'claims' -> key;
    if raw is null then
      continue;
    end if;

    -- SAML attribute values are always string[] (## Claims shape) ; OIDC
    -- claims are scalars. Take the first element either way.
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
