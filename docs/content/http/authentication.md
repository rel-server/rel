---
icon: material/shield-check
---

# Authentication

A session is a signed JWT, sitting in a `secure`, `httponly` cookie. Its only field rel
actually cares about is `role` — the Postgres role every query and route call in that session
runs as, applied with `SET ROLE`/`SET LOCAL ROLE` on the connection. Authentication in rel is
really just "how does the right value end up in that one claim" — there's no separate
authorization layer to configure on top; once `role` is set, Postgres's own grants and
row-level security decide what the request can actually do.

Anonymous access works the same way: with no valid session, a request runs as
`pg.query.anonymous_role` (default `~anonymous`) — a real Postgres role that must exist in the
database, or anonymous access is disabled outright and every unauthenticated request gets
`401`. There's no implicit "public" state in between.

## Minting a session

Any function called through [`/route`](index.md) can start a session by setting a `jwt`
field on its response. Like any other route, it takes a single `RelHttpRequest` argument (see
[Requests and responses](requests-responses.md)) — there's no separate, credentials-shaped
signature for a login function:

```sql
create function auth.login(req "RelHttpRequest") returns "RelHttpResponse"
language plpgsql
security definer
as $$
declare
  matched_role text;
begin
  select role into matched_role from auth.users
  where auth.users.username = (req->'body')->>'username'
    and auth.users.password_hash = crypt((req->'body')->>'password', auth.users.password_hash);

  if matched_role is null then
    raise exception 'Invalid credentials' using errcode = 'RS401';
  end if;

  return jsonb_build_object('jwt', jsonb_build_object('role', matched_role));
end;
$$;
```

Called as `POST /route/auth/login` with `{"username": "...", "password": "..."}` as the JSON
body.

rel fills in `iat`, `exp`, and `auth_time` itself — a function can only set `role` and any
custom claims it wants to carry alongside it. Setting `jwt: null` on a response clears the
session (logout). By default, *any* route function can set `jwt` and authenticate the caller
as any role; restrict that with `http.functions.allowed_auth` (a regexp against the function's
fully qualified name) once you have real login functions to point it at — e.g. `^auth\.`.

## OpenID Connect and SAML

For OIDC and SAML, rel drives the protocol itself — you only write the function that turns a
verified identity assertion into a role. Configure one named entry per provider:

```toml
[openid.google]
issuer = "https://accounts.google.com"
# client_id / client_secret default to /secrets/openid-google.id and
# /secrets/openid-google.secret — drop the two files there and skip these lines
callback_function = "auth.sso_callback"

[saml.corp_okta]
idp_metadata_url = "https://corp.okta.com/app/.../sso/saml/metadata"
callback_function = "auth.sso_callback"
```

`<name>` (`google`, `corp_okta` above) is yours to choose — it's not tied to any provider's
brand, so two Google Workspace tenants, or an OIDC and a SAML connection to the same IdP, are
just two differently-named entries. Each one gets its own routes:

- `GET /auth/oidc/{name}/login` / `GET /auth/oidc/{name}/callback`
- `GET /auth/saml/{name}/login` / `POST /auth/saml/{name}/acs`
- `GET /auth/saml/{name}/metadata` — this deployment's SP metadata, to hand to the IdP admin

Both protocols converge on the same callback function signature — one `claims` argument, one
`RelHttpResponse` return — regardless of which protocol produced it:

```sql
create function auth.sso_callback(claims jsonb) returns "RelHttpResponse"
language plpgsql
security definer
as $$
declare
  matched_role text;
begin
  -- claims->>'protocol' is 'oidc' or 'saml'; claims->'claims' holds the ID
  -- token's claims (OIDC) or every assertion attribute (SAML, each value a
  -- string[] even when single-valued). Look up whatever identifies this
  -- user in your own schema and decide a role from it.
  select role into matched_role
  from auth.users where auth.users.email = claims->'claims'->>'email';

  if matched_role is null then
    raise exception 'No account for this identity' using errcode = 'RS401';
  end if;

  return jsonb_build_object('jwt', jsonb_build_object('role', matched_role));
end;
$$;
```

rel doesn't interpret an email, a `groups` claim, or a SAML attribute for you — mapping
identity to a role is entirely your call, which is what keeps rel from assuming any particular
user-table shape. `openid.<name>.callback_function`/`saml.<name>.callback_function` each fall
back to `http.functions.sso_callback` when unset, so one function can serve every provider if
your role-mapping logic doesn't need to vary per provider.

A few things worth knowing before wiring this up in production:

- **`http.public_host` must be set** — a bare host like `app.example.com`, no scheme — so rel
  can build the exact `redirect_uri`/ACS URL each provider needs registered ahead of time.
- **SAML needs a stable certificate.** rel generates a self-signed one on first boot if
  `saml.certificate_path`/`saml.private_key_path` don't already exist, and reuses it silently
  on every later boot — swapping it breaks every IdP that was told to trust the old one, so
  back those files up the same way you'd back up any other credential.
- A misconfigured or not-yet-reachable provider doesn't fail startup — rel logs a warning and
  serves `503` from that provider's endpoints until it becomes reachable, so one broken IdP
  connection doesn't take the rest of the deployment down.

## Session lifecycle

A verified token gets renewed automatically once more than `jwt.renew_after` (default half)
of its own lifespan has elapsed — `role` and `auth_time` never change on renewal, only
`iat`/`exp`. Two separate limits bound a session: `jwt.max_age` bounds any single token (short,
so a stolen cookie is only useful briefly), and `jwt.max_session_age` bounds how long renewal
can keep extending the session overall, measured from the original `auth_time` — once it's
exceeded, the session can't be renewed anymore and the user has to fully re-authenticate.

For revocation before either of those would naturally expire it — a password change, a ban, an
admin-triggered logout — configure `http.functions.check_session`. rel calls it once per
authenticated request, before the role switch, with the full claims object as `jsonb`:

```sql
create function auth.check_session(jwt jsonb) returns void
language plpgsql
security definer
as $$
begin
  if exists (select 1 from auth.revoked_sessions where user_id = (jwt->>'user_id')::bigint) then
    raise exception 'Session revoked' using errcode = 'RS401';
  end if;
end;
$$;
```

Rejecting (`raise exception ... using errcode = 'RSxxx'`) clears the session and forces
re-authentication; returning normally lets it stand. rel doesn't require any particular claim
(a `jti`, a session id) for this to work — how you identify "this session" in your own revoked-
sessions table is up to whatever custom claims your login function put on the JWT.

## Deployment checklist

`SET ROLE` only succeeds when the connecting role is already a member of the role being
switched to — grant it explicitly for every role any JWT in the deployment may carry,
including the anonymous one:

```sql
grant "~anonymous" to query_user;
grant "editor" to query_user;
grant "admin" to query_user;
```

That grants `query_user` *membership* — the ability to `SET ROLE` into `~anonymous` at all. A
route function still needs its own `EXECUTE` grant, directly to `~anonymous` (or to a role it
belongs to), before an anonymous request can reach it:

```sql
grant execute on function guest_login("RelHttpRequest") to "~anonymous";
```

Skipping a grant doesn't fail at startup — introspection runs under the primary connection,
not the query role — so it surfaces as a `500` ("permission denied to set role") on the first
real request that needs it. A local superuser connection never hits this at all, which is why
it's easy to miss until deploying somewhere with a properly scoped role.

When anonymous access is enabled, rel also caches, once per discovered route at
introspection/reload time, whether the anonymous role can actually reach it — schema `USAGE`
plus an **explicit** `EXECUTE` grant, to the anonymous role itself or to a role it belongs to.
An anonymous request to a route the anonymous role can't reach this way is rejected with `401`
before the request body is even read.

This check deliberately doesn't credit `EXECUTE` the moment it's merely inherited from a grant
to `PUBLIC` — Postgres grants `EXECUTE` to `PUBLIC` by default on `create function` unless
default privileges were changed, so crediting it here would silently authorize anonymous
access to any route function nobody explicitly decided the anonymous role should reach. Grant
`EXECUTE` on any route function you actually want the anonymous role to call, the same way you
grant it any other role. Independently of this check, rel also warns (non-fatally) at
introspection/reload for every route reachable by `PUBLIC` at all, regardless of caller — see
[Best practices](../configuration/best-practices.md) for turning that default off entirely.

## Configuration reference

| Key | Default | Purpose |
|---|---|---|
| `jwt.secret` | generated into `/secrets/jwt-secret` | JWT signing secret |
| `jwt.cookie_name` | `accesstoken` | cookie carrying the JWT |
| `jwt.algorithm` | `HS256` | one of `HS256`/`HS384`/`HS512` |
| `jwt.same_site` | `Lax` | `SameSite` on the JWT cookie |
| `jwt.max_age` | `1800` (30 min) | freshly-minted token lifetime, seconds |
| `jwt.renew_after` | `0.5` | fraction of lifetime elapsed before auto-renewal |
| `jwt.max_session_age` | `604800` (7 days) | hard ceiling on total session lifetime |
| `pg.query.anonymous_role` | `~anonymous` | role for requests with no valid session |
| `http.functions.check_session` | unset | function called per authenticated request to allow revocation |
| `http.functions.allowed_auth` | unset (unrestricted) | regexp restricting which functions may set `jwt` |
| `http.public_host` | unset | this deployment's externally-reachable host, required for OIDC/SAML |
| `openid.<name>.issuer` | — | required; the OIDC issuer URL |
| `openid.<name>.scopes` | `openid, email, profile` | requested OIDC scopes |
| `openid.<name>.fetch_userinfo` | `false` | also call the userinfo endpoint after token exchange |
| `openid.<name>.callback_function` / `saml.<name>.callback_function` | unset, falls back to `http.functions.sso_callback` | function invoked with the verified claims |
| `saml.<name>.idp_metadata_url` | — | required; fetched once at startup |
| `saml.<name>.force_signed_requests` | `true` | sign outgoing `AuthnRequest`s |
| `saml.certificate_path` / `saml.private_key_path` | generated on first boot | this deployment's SP certificate/key, shared across every `saml.<name>` entry |

See [Configuration](../configuration/index.md) for how these values, secrets included, get
supplied across environment variables, config files, and generated files — including a couple
of keys not repeated here (`openid.<name>.client_id`/`client_secret`,
`openid.<name>.public_host`/`saml.<name>.public_host`) and every other key rel understands.
