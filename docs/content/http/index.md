---
icon: material/routes
---

# HTTP routes

`/rel` answers one shape: a query tree, read or written back. Anything that doesn't fit that
shape — logging a guest in, sending a confirmation email, running a multi-step booking
workflow with real side effects — goes through `/route` instead. A route is an ordinary
Postgres function, written in whatever language Postgres supports (`plpgsql`, `sql`, `plv8`,
...), that rel discovers and calls directly over HTTP.

## What makes a function a route

A Postgres function is discovered as a route, reachable at `/route/<schema>/<function_name>`,
when all of the following hold:

- its name does not start with `_`;
- its arguments are one of the recognized shapes — no arguments, a single
  [`RelHttpRequest`](requests-responses.md) argument, or `RelHttpRequest` plus one of the
  extra body-parameter shapes described in [File uploads](uploads.md#receiving-raw-bytes);
- its return type is [`RelHttpResponse`](requests-responses.md) or a
  [mimetype domain](static-files.md#returning-binary-or-text-content-directly);
- if `http.functions.allowed_routes` is set, its fully qualified name matches that regexp
  (see [Configuration](../configuration/index.md)).

```sql
create function hotel.guest_login(req "RelHttpRequest") returns "RelHttpResponse"
language plpgsql as $$
declare
  guest_row hotel.guests;
begin
  select * into guest_row from hotel.guests
  where email = (req->'body')->>'email';

  if not found then
    raise exception 'Invalid credentials' using errcode = 'RS401';
  end if;

  return jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object('id', guest_row.id, 'name', guest_row.first_name),
    'jwt', jsonb_build_object('role', 'guest', 'guest_id', guest_row.id)
  );
end;
$$;
```

`hotel.guest_login` becomes reachable at `POST /route/hotel/guest_login` (or any verb — see
below). A function name starting with `_` is never discovered as a route at all — the way to
write a private helper a route function calls internally without exposing it.

By default, any route function can set `jwt` on its response and thereby authenticate the
caller as any role. Restrict that with `http.functions.allowed_auth`, scoped to your actual
login functions — see [Configuration](../configuration/index.md). The rest of a session's
lifecycle (claims, renewal, roles) is covered in [Authentication](authentication.md).

## Restricting a route to one HTTP verb

Suffix a function name with `__GET`, `__POST`, `__PUT`, `__PATCH`, or `__DELETE` (case
doesn't matter) to restrict it to that one HTTP verb:

- an unsuffixed function is the fallback for any verb without a more specific, suffixed match;
- conforming to web semantics — a `__GET` route must not mutate state — is the function
  author's responsibility; rel does not enforce it, but does rely on it for its own CSRF
  posture (`SameSite=Lax` cookies, see [Authentication](authentication.md)).

## Ambiguous routes

Two or more discovered functions sharing the same schema, base name (once any `__VERB`
suffix is stripped), and resolved verb collide — neither becomes the registered route at that
key. Route discovery logs an error naming every colliding function; this is a discovery-time
warning, not a fatal error, but the affected key stays unroutable until the collision is
resolved in the schema itself.

## Errors are just exceptions

Raise a Postgres exception with an `RSxxx` error code and rel turns it directly into that HTTP
status, with the raised message as the body — no separate error-response plumbing to write:

```sql
raise exception 'Access denied' using errcode = 'RS401';
raise exception 'Room not found' using errcode = 'RS404';
```

Any other error surfaces as a `500`, unless it's a Postgres error code rel already maps to an
obvious HTTP status (a permission-denied error becomes `401`, for instance).

## In this section

- **[Requests and responses](requests-responses.md)** — the `RelHttpRequest`/`RelHttpResponse`
  domains.
- **[Rendering HTML with templates](templates.md)** — server-side Jet templates, the CSP
  nonce for trusted inline scripts, and `extends`/`block` layouts.
- **[File uploads](uploads.md)** — receiving raw bytes, or choosing where an upload lands on
  disk without routing its bytes through Postgres.
- **[Static files](static-files.md)** — serving a directory at `/static/*`, gating part of it
  behind a database check, and returning binary/text content directly from a route.
- **[CORS and CSP](cors-csp.md)** — cross-origin access and the response security policy.
- **[Authentication](authentication.md)** — sessions, OpenID Connect, SAML, and roles.

See [Configuration](../configuration/index.md) for the full list of `http.*` settings,
including request size limits and the domain names rel looks for if you ever need to rename
them.
